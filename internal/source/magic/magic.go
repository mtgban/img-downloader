// Package magic provides the Magic want-list, sourced from the public MTGJSON
// and Scryfall bulk exports.
//
// This is the original mirror path, unchanged in behaviour: MTGJSON enumerates
// the sets, their cards' scryfallIds and their sealed products' TCGplayer ids,
// and Scryfall's default_cards bulk file supplies the front image URL for each
// scryfallId. It is the one game not sourced from mtgban's own datastore.
package magic

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"

	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/mtgjson"
	"github.com/mtgban/img-downloader/internal/scryfall"
	"github.com/mtgban/img-downloader/internal/source"
)

// AllPrintingsURL is the MTGJSON export the set and card enumeration comes from.
const AllPrintingsURL = "https://mtgjson.com/api/v5/AllPrintings.json.gz"

// Provider builds the Magic want-list.
type Provider struct {
	// HTTP is the client used for both bulk downloads; nil means
	// http.DefaultClient.
	HTTP *http.Client
	// AllPrintingsURL overrides the MTGJSON export location, for tests.
	AllPrintingsURL string
	// Log receives the two source-quality counts this provider reports; nil
	// means log.Default().
	Log *log.Logger
}

// Game implements source.Provider.
func (p *Provider) Game() source.Game { return source.Magic }

// IsSealedKey implements source.SealedAware; Magic mirrors TCGplayer's sealed
// product images alongside singles.
func (p *Provider) IsSealedKey(key string) bool { return mirror.IsSealedKey(key) }

// BuildWant implements source.Provider. It fetches both bulk sources and joins
// them into the want-list.
func (p *Provider) BuildWant(ctx context.Context, setsFilter map[string]bool) (source.Want, error) {
	logger := p.Log
	if logger == nil {
		logger = log.Default()
	}

	scryURL, err := p.loadScryfallURLs(ctx)
	if err != nil {
		return nil, err
	}

	sets, err := p.loadMTGJSONSets(ctx)
	if err != nil {
		return nil, err
	}

	want, missing, invalidSealed := BuildWant(sets, scryURL, setsFilter)
	logger.Printf("%d scryfall IDs referenced by mtgjson had no bulk-data match", len(missing))
	logger.Printf("%d sealed refs had an invalid set code or tcgplayer id", invalidSealed)
	return want, nil
}

// loadScryfallURLs resolves the default_cards bulk file and streams it into an
// id -> front image URL map.
func (p *Provider) loadScryfallURLs(ctx context.Context) (map[string]string, error) {
	httpClient := p.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	client := scryfall.Client{HTTP: httpClient}
	uri, err := client.DefaultCardsURI(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", scryfall.UserAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scryfall bulk download: status %d", resp.StatusCode)
	}

	urls := map[string]string{}
	err = scryfall.StreamCards(resp.Body, func(c scryfall.BulkCard) error {
		if u := c.FrontImageURL(); u != "" {
			urls[c.ID] = u
		}
		return nil
	})
	if err != nil {
		return nil, interrupted(ctx, err)
	}
	return urls, nil
}

// loadMTGJSONSets fetches and streams AllPrintings.json.gz into a slice of SetImages.
func (p *Provider) loadMTGJSONSets(ctx context.Context) ([]mtgjson.SetImages, error) {
	httpClient := p.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	url := p.AllPrintingsURL
	if url == "" {
		url = AllPrintingsURL
	}

	rc, err := mtgjson.Fetch(ctx, httpClient, url)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var sets []mtgjson.SetImages
	err = mtgjson.StreamSets(rc, func(s mtgjson.SetImages) error {
		sets = append(sets, s)
		return nil
	})
	if err != nil {
		return nil, interrupted(ctx, err)
	}
	return sets, nil
}

// interrupted prefers ctx's error over err. Cancelling a stream mid-read leaves
// the decoder holding a truncated record, so the parse error it reports would
// otherwise mask the interrupt that caused it.
func interrupted(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// sealedKeyPattern mirrors the website's offlineapi sealed key regex.
var sealedKeyPattern = regexp.MustCompile(`^p-[0-9A-Z]{2,6}-[0-9]+$`)

// BuildWant assembles the wanted image map from mtgjson sets and scryfall URLs.
// invalidSealed counts sealed refs with a malformed set code or tcgplayer id.
func BuildWant(sets []mtgjson.SetImages, scryURL map[string]string, setsFilter map[string]bool) (want source.Want, missing []string, invalidSealed int) {
	want = source.Want{}
	for _, s := range sets {
		if setsFilter != nil && !setsFilter[s.Code] {
			continue
		}
		for _, id := range s.ScryfallIDs {
			srcURL, ok := scryURL[id]
			if !ok {
				missing = append(missing, id)
				continue
			}
			// only well formed scryfall ids become object paths
			objectPath, err := mirror.SingleObjectPath(id)
			if err != nil {
				continue
			}
			want[id] = mirror.Image{Key: id, URL: srcURL, ObjectPath: objectPath, SetCode: s.Code}
		}
		for _, sealed := range s.Sealed {
			key := mirror.SealedKey(s.Code, sealed.TcgplayerProductID)
			if !sealedKeyPattern.MatchString(key) {
				invalidSealed++
				continue
			}
			want[key] = mirror.Image{
				Key:        key,
				URL:        fmt.Sprintf("https://product-images.tcgplayer.com/%s.jpg", sealed.TcgplayerProductID),
				ObjectPath: mirror.SealedObjectPath(s.Code, sealed.TcgplayerProductID),
				SetCode:    s.Code,
			}
		}
	}
	sort.Strings(missing)
	return want, missing, invalidSealed
}
