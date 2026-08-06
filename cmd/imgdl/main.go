// Command imgdl mirrors Scryfall and MTGJSON sealed images into a bucket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mtgban/simplecloud"
	"github.com/the-muppet2/img-downloader/internal/mirror"
	"github.com/the-muppet2/img-downloader/internal/mtgjson"
	"github.com/the-muppet2/img-downloader/internal/scryfall"
)

const allPrintingsURL = "https://mtgjson.com/api/v5/AllPrintings.json.gz"

func main() {
	bucketFlag := flag.String("bucket", "", "bucket to mirror into, b2://name/prefix or a local dir (required)")
	setsFlag := flag.String("sets", "", "CSV of set codes to mirror, empty means all sets")
	dryRun := flag.Bool("dry-run", false, "print the fetch plan without fetching or writing")
	skipSealed := flag.Bool("skip-sealed", false, "skip the TCGplayer sealed product pass")
	flag.Parse()

	if *bucketFlag == "" {
		log.Fatal("-bucket is required")
	}

	ctx := context.Background()
	if err := run(ctx, *bucketFlag, *setsFlag, *dryRun, *skipSealed); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, bucketFlag, setsFlag string, dryRun, skipSealed bool) error {
	bucket, base, err := openBucket(ctx, bucketFlag)
	if err != nil {
		return err
	}

	scryURL, err := loadScryfallURLs(ctx)
	if err != nil {
		return err
	}

	sets, err := loadMTGJSONSets(ctx)
	if err != nil {
		return err
	}
	if skipSealed {
		for i := range sets {
			sets[i].Sealed = nil
		}
	}

	want, missing := mirror.BuildWant(sets, scryURL, parseSets(setsFlag))
	log.Printf("%d scryfall IDs referenced by mtgjson had no bulk-data match", len(missing))

	opts := mirror.Opts{Bucket: bucket, Base: base, Want: want, DryRun: dryRun, Log: log.Default()}
	result, runErr := mirror.Run(ctx, opts)
	fmt.Printf("pending=%d fetched=%d fetchFailed=%d bundlesRebuilt=%d\n",
		result.Pending, result.Fetched, result.FetchFailed, result.BundlesRebuilt)
	if runErr != nil {
		// exit 1 on a partial fetch; the manifest and state were still saved
		return runErr
	}
	return nil
}

// loadScryfallURLs resolves the default_cards bulk file and streams it into an id -> normal front URL map.
func loadScryfallURLs(ctx context.Context) (map[string]string, error) {
	client := scryfall.Client{}
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scryfall bulk download: status %d", resp.StatusCode)
	}

	urls := map[string]string{}
	err = scryfall.StreamCards(resp.Body, func(c scryfall.BulkCard) error {
		if u := c.NormalFrontURL(); u != "" {
			urls[c.ID] = u
		}
		return nil
	})
	return urls, err
}

// loadMTGJSONSets fetches and streams AllPrintings.json.gz into a slice of SetImages.
func loadMTGJSONSets(ctx context.Context) ([]mtgjson.SetImages, error) {
	rc, err := mtgjson.Fetch(ctx, http.DefaultClient, allPrintingsURL)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var sets []mtgjson.SetImages
	err = mtgjson.StreamSets(rc, func(s mtgjson.SetImages) error {
		sets = append(sets, s)
		return nil
	})
	return sets, err
}

// parseSets splits a CSV of set codes into an uppercased filter set, nil when empty.
func parseSets(csv string) map[string]bool {
	if csv == "" {
		return nil
	}
	out := map[string]bool{}
	for _, part := range strings.Split(csv, ",") {
		code := strings.ToUpper(strings.TrimSpace(part))
		if code != "" {
			out[code] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// openBucket resolves raw into a bucket and its base path within it.
func openBucket(ctx context.Context, raw string) (simplecloud.ReadWriter, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", err
	}
	switch {
	case u.Scheme == "":
		return &simplecloud.FileBucket{}, raw, nil
	case len(u.Scheme) == 1:
		// a one letter scheme is a Windows drive letter, e.g. C:/x
		return nil, "", fmt.Errorf("openBucket: Windows absolute paths are not supported: %s", raw)
	case u.Scheme == "b2":
		key := os.Getenv("B2_ACCESS_KEY")
		secret := os.Getenv("B2_ACCESS_SECRET")
		if key == "" || secret == "" {
			return nil, "", errors.New("openBucket: B2_ACCESS_KEY and B2_ACCESS_SECRET must be set for b2:// buckets")
		}
		b2Bucket, err := simplecloud.NewB2Client(ctx, key, secret, u.Host)
		if err != nil {
			return nil, "", err
		}
		return b2Bucket, strings.TrimPrefix(u.Path, "/"), nil
	default:
		return nil, "", fmt.Errorf("openBucket: unsupported bucket scheme %q", u.Scheme)
	}
}
