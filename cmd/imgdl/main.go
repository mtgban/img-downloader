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
	"os/signal"
	"strings"
	"syscall"

	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/mtgjson"
	"github.com/mtgban/img-downloader/internal/scryfall"
	"github.com/mtgban/simplecloud"
)

const allPrintingsURL = "https://mtgjson.com/api/v5/AllPrintings.json.gz"

func main() {
	setsFlag := flag.String("sets", "", "CSV of set codes to mirror, empty means all sets")
	dryRun := flag.Bool("dry-run", false, "print the fetch plan without fetching or writing")
	skipSealed := flag.Bool("skip-sealed", false, "skip the TCGplayer sealed product pass")
	retryMissing := flag.Bool("retry-missing", false, "ask again for images a source previously answered it had none of")
	rebuildBundles := flag.Bool("rebuild-bundles", false, "rebuild every set bundle, even where the manifest already lists a current hash")
	flag.Parse()

	bucketEnv, err := requireBucketEnv()
	if err != nil {
		log.Fatal(err)
	}

	if err := run(signalContext(), bucketEnv, *setsFlag, *dryRun, *skipSealed, *retryMissing, *rebuildBundles); err != nil {
		if errors.Is(err, context.Canceled) {
			// state is snapshotted as the crawl goes, so a rerun picks up where this left off
			log.Print("interrupted, progress saved; rerun the same command to resume")
			os.Exit(130)
		}
		log.Fatal(err)
	}
}

// signalContext returns a context cancelled by the first SIGINT or SIGTERM.
// Default handling is restored once it fires, so a second signal kills a run
// that is too wedged to unwind on its own.
func signalContext() context.Context {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx
}

// requireBucketEnv reads B2_BUCKET and errors clearly if it is unset.
func requireBucketEnv() (string, error) {
	bucket := os.Getenv("B2_BUCKET")
	if bucket == "" {
		return "", errors.New("B2_BUCKET env variable is required, e.g. b2://mtgban-images/magic or a local dir")
	}
	return bucket, nil
}

func run(ctx context.Context, bucketEnv, setsFlag string, dryRun, skipSealed, retryMissing, rebuildBundles bool) error {
	bucket, base, err := openBucket(ctx, bucketEnv)
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

	want, missing, invalidSealed := mirror.BuildWant(sets, scryURL, parseSets(setsFlag))
	log.Printf("%d scryfall IDs referenced by mtgjson had no bulk-data match", len(missing))
	log.Printf("%d sealed refs had an invalid set code or tcgplayer id", invalidSealed)

	opts := mirror.Opts{Bucket: bucket, Base: base, Want: want, DryRun: dryRun, SkipSealed: skipSealed, RetryMissing: retryMissing, RebuildBundles: rebuildBundles, Log: log.Default()}
	result, runErr := mirror.Run(ctx, opts)
	fmt.Printf("pending=%d fetched=%d notPublished=%d fetchFailed=%d bundlesRebuilt=%d\n",
		result.Pending, result.Fetched, result.NotPublished, result.FetchFailed, result.BundlesRebuilt)
	if runErr != nil {
		// exit 1 on a partial fetch; the manifest and state were still saved
		return runErr
	}
	return nil
}

// loadScryfallURLs resolves the default_cards bulk file and streams it into an id -> front image URL map.
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

// interrupted prefers ctx's error over err. Cancelling a stream mid-read leaves
// the decoder holding a truncated record, so the parse error it reports would
// otherwise mask the interrupt that caused it.
func interrupted(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
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
	if err != nil {
		return nil, interrupted(ctx, err)
	}
	return sets, nil
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
