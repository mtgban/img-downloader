// Package datastore provides a want-list sourced from mtgban's own datastore,
// for the games that have no public bulk export to build one from.
//
// It reads the same document the website reads — the datastore file a
// deployment loads with mtgmatcher — and takes each card's product and its
// "full" image URL straight from it. Nothing here parses that document by hand:
// mtgmatcher.Open decodes it, so this tool and the website agree on the schema
// by construction rather than by a copy of it kept in step.
package datastore

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/mtgban/go-mtgban/mtgmatcher"
	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/source"
	"github.com/mtgban/simplecloud"
	// the loaders come in through internal/source, which gathers them in one
	// import; mtgmatcher resolves a game by name only if one is registered
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
	// clashes counts finishes whose key already holds another image or set,
	// each of which leaves one of them showing the wrong picture
	clashes int
}

// wantFromBackend walks the loaded datastore into a want-list.
func (p *Provider) wantFromBackend(backend *mtgmatcher.Backend, setsFilter map[string]bool, logger *log.Logger) (source.Want, error) {
	want := source.Want{}
	var st stats

	usable := map[string]bool{}
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
		usable[code] = true

		for _, product := range set.SealedProduct {
			p.addSealed(want, code, product, backend, &st)
		}
	}

	// Singles are walked the way the website's catalog walks them, one entry
	// per finish, and sorted so a clash resolves the same way every run.
	for _, uuid := range slices.Sorted(slices.Values(backend.GetUUIDs())) {
		co := backend.UUIDs[uuid]
		if co == nil || co.Sealed || !usable[co.SetCode] {
			continue
		}
		p.addSingle(want, co, &st, logger)
	}

	logger.Printf("datastore: %d images wanted; skipped %d cards with no %s image, %d with an unusable id, %d sealed products with no image; %d cards clashed on a key",
		len(want), st.noImage, imageKind, st.unusableID, st.sealedNoImg, st.clashes)
	if len(want) == 0 {
		return nil, fmt.Errorf("datastore: %s datastore yielded no images; refusing to treat that as an empty mirror", p.game)
	}
	return want, nil
}

// singleKey is the key a single's image is filed under: the TCGplayer product
// id all finishes of the product share or, where the card names no product,
// the key its finishes share (mtgmatcher.PrintingKey). The website's offline
// catalog asks for the same expression (internal/offlineapi,
// datastoreImageKey); change both together.
//
// It reads fields, never the uuid's shape, which the datastore may respell.
// Keyed apart, a set level uuid here and a cut uuid there, 115,429 of the
// 140,047 singles the website listed asked for a key this never filed.
func singleKey(co *mtgmatcher.CardObject) string {
	return mtgmatcher.ProductKeyOf(co.Identifiers, mtgmatcher.PrintingKey(co.Card))
}

// addSingle adds one entry's front image to want, once per key: the other
// finishes of the product land on the same key and are only checked against
// it.
func (p *Provider) addSingle(want source.Want, co *mtgmatcher.CardObject, st *stats, logger *log.Logger) {
	srcURL := co.Images[imageKind]
	if srcURL == "" {
		st.noImage++
		return
	}
	key := singleKey(co)
	if have, found := want[key]; found {
		if have.URL != srcURL || have.SetCode != co.SetCode {
			st.clashes++
			logger.Printf("datastore: %s wants %s (%s) under %s, which holds %s (%s)", co.UUID, srcURL, co.SetCode, key, have.URL, have.SetCode)
		}
		return
	}
	objectPath, err := mirror.GameSingleObjectPath(key, variant)
	if err != nil {
		st.unusableID++
		return
	}
	want[key] = mirror.Image{Key: key, URL: srcURL, ObjectPath: objectPath, SetCode: co.SetCode}
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
	objectPath, err := mirror.GameSealedObjectPath(setCode, product.UUID)
	if err != nil {
		st.unusableID++
		return
	}
	key := mirror.GameSealedKey(product.UUID)
	want[key] = mirror.Image{Key: key, URL: srcURL, ObjectPath: objectPath, SetCode: setCode}
}
