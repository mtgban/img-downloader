// Package datastore provides a want-list sourced from mtgban's own datastore,
// for the games that have no public bulk export to build one from.
//
// It reads the same document the website reads — the datastore file a
// deployment loads with mtgmatcher — and takes each card's id and its "full"
// image URL straight from it. Nothing here parses that document by hand:
// mtgmatcher.Open decodes it, so this tool and the website agree on the schema
// by construction rather than by a copy of it kept in step.
package datastore

import (
	"context"
	"fmt"
	"log"

	"github.com/mtgban/go-mtgban/mtgmatcher"
	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/source"
	"github.com/mtgban/simplecloud"

	// the games whose datastores this provider can open; mtgmatcher resolves a
	// game by name only if its package has registered a loader
	_ "github.com/mtgban/go-mtgban/mtgmatcher/lorcana"
	_ "github.com/mtgban/go-mtgban/mtgmatcher/riftbound"
)

// imageKind is the mtgmatcher Images key mirrored. A datastore card carries
// "full" and "thumbnail"; full is the card-sized image, the counterpart of what
// the Magic path takes from Scryfall.
const imageKind = "full"

// variant names the stored image's flavour in the object path, as "grid" does
// for Magic. A datastore game publishes one image per card rather than a set of
// encodes, so there is only ever this one.
const variant = "full"

// Config locates the datastore document to build the want-list from.
type Config struct {
	// Bucket holds the datastore document. It is opened separately from the
	// image bucket, because the datastore lives with the site's data rather
	// than with the mirrored images.
	Bucket simplecloud.Reader
	// Path is the document's object path within Bucket. simplecloud
	// decompresses by extension, so an .xz or .gz suffix is handled here.
	Path string
	Log  *log.Logger
}

// Provider builds a want-list from one game's datastore.
type Provider struct {
	game source.Game
	cfg  Config
}

// New returns a Provider for game. It fails fast on a game mtgmatcher has no
// loader registered for, rather than at the point the document is opened.
func New(game source.Game, cfg Config) (*Provider, error) {
	if cfg.Bucket == nil {
		return nil, fmt.Errorf("datastore: no bucket configured for %s", game)
	}
	if cfg.Path == "" {
		return nil, fmt.Errorf("datastore: no datastore path configured for %s", game)
	}
	if !slicesContains(mtgmatcher.RegisteredGames(), string(game)) {
		return nil, fmt.Errorf("datastore: mtgmatcher has no loader for %q, registered: %v",
			game, mtgmatcher.RegisteredGames())
	}
	return &Provider{game: game, cfg: cfg}, nil
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Game implements source.Provider.
func (p *Provider) Game() source.Game { return p.game }

// IsSealedKey implements source.SealedAware. Sealed keys keep the Magic p-
// prefix so one key namespace serves every game and singles stay
// distinguishable from products without consulting the game.
func (p *Provider) IsSealedKey(key string) bool { return mirror.IsSealedKey(key) }

// BuildWant implements source.Provider.
func (p *Provider) BuildWant(ctx context.Context, setsFilter map[string]bool) (source.Want, error) {
	logger := p.cfg.Log
	if logger == nil {
		logger = log.Default()
	}

	reader, err := simplecloud.InitReader(ctx, p.cfg.Bucket, p.cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("datastore: open %s: %w", p.cfg.Path, err)
	}
	defer reader.Close()

	// Open rather than LoadDatastore: this tool mirrors one game per run and
	// has no use for mtgmatcher's global backend, and loading a game's data
	// into a process-wide singleton is how a second game would silently
	// inherit the first one's cards.
	backend, err := mtgmatcher.Open(string(p.game), reader)
	if err != nil {
		return nil, fmt.Errorf("datastore: load %s datastore: %w", p.game, err)
	}

	return p.wantFromBackend(backend, setsFilter, logger)
}

// stats counts the cards a build skipped, so a run says how much of the
// datastore it could not use rather than quietly mirroring less than it should.
type stats struct {
	noImage     int
	unusableID  int
	sealedNoImg int
}

// wantFromBackend walks the loaded datastore into a want-list.
func (p *Provider) wantFromBackend(backend *mtgmatcher.Backend, setsFilter map[string]bool, logger *log.Logger) (source.Want, error) {
	want := source.Want{}
	var st stats

	for code, set := range backend.Sets {
		if set == nil {
			continue
		}
		// the set code becomes a path segment for sealed and is the manifest
		// key for everything, so a set that cannot be named is skipped whole
		if !mirror.SafeSegment(code) {
			logger.Printf("datastore: skipping set %q, its code is not usable as a manifest key", code)
			continue
		}
		if setsFilter != nil && !setsFilter[code] {
			continue
		}

		for _, cards := range [][]mtgmatcher.Card{set.Cards, set.Tokens} {
			for _, card := range cards {
				p.addSingle(want, code, card, &st)
			}
		}
		for _, product := range set.SealedProduct {
			p.addSealed(want, code, product, backend, &st)
		}
	}

	logger.Printf("datastore: %d images wanted; skipped %d cards with no %s image, %d with an unusable id, %d sealed products with no image",
		len(want), st.noImage, imageKind, st.unusableID, st.sealedNoImg)
	if len(want) == 0 {
		return nil, fmt.Errorf("datastore: %s datastore yielded no images; refusing to treat that as an empty mirror", p.game)
	}
	return want, nil
}

// addSingle adds one card's front image to want.
func (p *Provider) addSingle(want source.Want, setCode string, card mtgmatcher.Card, st *stats) {
	srcURL := card.Images[imageKind]
	if srcURL == "" {
		st.noImage++
		return
	}
	// The key is the card's own datastore id, not the image URL's basename.
	// Magic can use the basename because a Scryfall URL is named for the card;
	// these games' URLs are their CDN's filenames, which name nothing the rest
	// of the system knows.
	//
	// This is the set level card, so its uuid is the printing's base id. The
	// per finish uuids that hang off it (Lorcana's "460_f", Riftbound's
	// "ogn-066-298_foil") all share this one image, so the printing is
	// mirrored once and a reader holding a finish uuid trims at the last
	// underscore to find it. Mirroring per finish instead would store the
	// same bytes two or three times over.
	objectPath, err := mirror.GameSingleObjectPath(card.UUID, variant)
	if err != nil {
		st.unusableID++
		return
	}
	want[card.UUID] = mirror.Image{Key: card.UUID, URL: srcURL, ObjectPath: objectPath, SetCode: setCode}
}

// addSealed adds one sealed product's image to want. The product's image lives
// on its card entry rather than on the product record, so it is looked up by
// uuid.
func (p *Provider) addSealed(want source.Want, setCode string, product mtgmatcher.SealedProduct, backend *mtgmatcher.Backend, st *stats) {
	co := backend.UUIDs[product.UUID]
	if co == nil {
		st.sealedNoImg++
		return
	}
	srcURL := co.Images[imageKind]
	if srcURL == "" {
		st.sealedNoImg++
		return
	}
	objectPath, err := mirror.GameSealedObjectPath(product.UUID)
	if err != nil {
		st.unusableID++
		return
	}
	key := mirror.GameSealedKey(product.UUID)
	want[key] = mirror.Image{Key: key, URL: srcURL, ObjectPath: objectPath, SetCode: setCode}
}
