// Command imgdl mirrors a card game's card and sealed product images into a bucket.
//
// Which game is mirrored, and therefore where its card list and image URLs
// come from, is chosen with -game. Magic comes from the public MTGJSON and
// Scryfall bulk exports; every other game comes from mtgban's own datastore.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/source"
	"github.com/mtgban/img-downloader/internal/source/datastore"
	"github.com/mtgban/img-downloader/internal/source/magic"
	"github.com/mtgban/simplecloud"
)

// datastoreEnv names the env var locating a non-Magic game's datastore
// document, the counterpart of the website's datastore_path config key.
const datastoreEnv = "IMGDL_DATASTORE"

// opts is one invocation's configuration, from flags and the environment.
type opts struct {
	game           source.Game
	bucket         string
	sets           string
	dryRun         bool
	skipSealed     bool
	retryMissing   bool
	rebuildBundles bool
}

func main() {
	gameFlag := flag.String("game", envOr("IMGDL_GAME", string(source.Magic)),
		"card game to mirror: "+strings.Join(gameNames(), ", "))
	setsFlag := flag.String("sets", "", "CSV of set codes to mirror, empty means all sets")
	dryRun := flag.Bool("dry-run", false, "print the fetch plan without fetching or writing")
	skipSealed := flag.Bool("skip-sealed", false, "skip the sealed product pass")
	retryMissing := flag.Bool("retry-missing", false, "ask again for images a source previously answered it had none of")
	rebuildBundles := flag.Bool("rebuild-bundles", false, "rebuild every set bundle, even where the manifest already lists a current hash")
	flag.Parse()

	game, err := source.ParseGame(*gameFlag)
	if err != nil {
		log.Fatal(err)
	}

	bucketEnv, err := requireBucketEnv()
	if err != nil {
		log.Fatal(err)
	}

	cfg := opts{
		game:           game,
		bucket:         bucketEnv,
		sets:           *setsFlag,
		dryRun:         *dryRun,
		skipSealed:     *skipSealed,
		retryMissing:   *retryMissing,
		rebuildBundles: *rebuildBundles,
	}

	if err := run(signalContext(), cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			// state is snapshotted as the crawl goes, so a rerun picks up where this left off
			log.Print("interrupted, progress saved; rerun the same command to resume")
			os.Exit(130)
		}
		log.Fatal(err)
	}
}

// envOr returns the environment value for key, or def when it is unset or empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// gameNames lists the mirrorable games, for the -game flag's help text.
func gameNames() []string {
	out := make([]string, 0, len(source.Games()))
	for _, g := range source.Games() {
		out = append(out, string(g))
	}
	return out
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

// newProvider returns the want-list provider for game.
//
// Magic is the one game built from public bulk data; the rest read their card
// lists and image URLs from mtgban's own datastore. That datastore is a
// separate location from the image bucket — it is the site's data, not the
// mirror's — so it is opened from its own URL rather than resolved under the
// bucket being written to.
func newProvider(ctx context.Context, game source.Game) (source.Provider, error) {
	if game == source.Magic {
		return &magic.Provider{Log: log.Default()}, nil
	}

	raw := os.Getenv(datastoreEnv)
	if raw == "" {
		return nil, fmt.Errorf("%s is required to mirror %s, e.g. b2://mtgban-datastore/%s/allCards.json or a local file",
			datastoreEnv, game, game)
	}
	bucket, objectPath, err := openBucket(ctx, raw, datastoreCreds)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", datastoreEnv, err)
	}
	return datastore.New(game, datastore.Config{
		Bucket: bucket,
		Path:   objectPath,
		Log:    log.Default(),
	})
}

func run(ctx context.Context, cfg opts) error {
	bucket, base, err := openBucket(ctx, cfg.bucket, imageCreds)
	if err != nil {
		return err
	}
	log.Printf("mirroring game %s into %s", cfg.game, base)

	// before anything is fetched, so a misdirected run costs nothing
	if err := mirror.ClaimGame(ctx, bucket, base, string(cfg.game), cfg.dryRun); err != nil {
		return err
	}

	provider, err := newProvider(ctx, cfg.game)
	if err != nil {
		return err
	}

	sealed, sealedAware := provider.(source.SealedAware)
	if !sealedAware && cfg.skipSealed {
		return fmt.Errorf("-skip-sealed is not applicable to %s, which mirrors no sealed images", cfg.game)
	}

	want, err := provider.BuildWant(ctx, parseSets(cfg.sets))
	if err != nil {
		return err
	}
	source.LogWant(log.Default(), cfg.game, want)

	mirrorOpts := mirror.Opts{
		Bucket:         bucket,
		Base:           base,
		Want:           want,
		DryRun:         cfg.dryRun,
		SkipSealed:     cfg.skipSealed,
		RetryMissing:   cfg.retryMissing,
		RebuildBundles: cfg.rebuildBundles,
		Log:            log.Default(),
	}
	if sealedAware {
		mirrorOpts.IsSealedKey = sealed.IsSealedKey
	}

	result, runErr := mirror.Run(ctx, mirrorOpts)
	fmt.Printf("game=%s pending=%d fetched=%d notPublished=%d fetchFailed=%d bundlesRebuilt=%d\n",
		cfg.game, result.Pending, result.Fetched, result.NotPublished, result.FetchFailed, result.BundlesRebuilt)
	if runErr != nil {
		// exit 1 on a partial fetch; the manifest and state were still saved
		return runErr
	}
	return nil
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

// credentials names the environment a bucket's B2 key pair is read from.
//
// The image bucket and the datastore are separate buckets with separate keys,
// because they want separate rights: the mirror writes images and only reads
// the datastore, and a B2 application key is scoped to one bucket anyway.
type credentials struct{ keyEnv, secretEnv string }

var (
	imageCreds     = credentials{"B2_ACCESS_KEY", "B2_ACCESS_SECRET"}
	datastoreCreds = credentials{"B2_DATASTORE_ACCESS_KEY", "B2_DATASTORE_ACCESS_SECRET"}
)

// read returns the pair, and whether both halves were set.
func (c credentials) read() (string, string, bool) {
	key := os.Getenv(c.keyEnv)
	secret := os.Getenv(c.secretEnv)
	return key, secret, key != "" && secret != ""
}

// openBucket resolves raw into a bucket and its base path within it, opening a
// b2:// url with the given key pair.
func openBucket(ctx context.Context, raw string, creds credentials) (simplecloud.ReadWriter, string, error) {
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
		key, secret, ok := creds.read()
		if !ok {
			// The datastore falls back to the image key, for a deployment
			// running one key across both buckets. Naming both pairs keeps
			// that from reading as though only one of them were an option.
			if creds == datastoreCreds {
				var fellBack bool
				key, secret, fellBack = imageCreds.read()
				if !fellBack {
					return nil, "", fmt.Errorf("openBucket: %s and %s (or %s and %s) must be set to read a b2:// datastore",
						datastoreCreds.keyEnv, datastoreCreds.secretEnv, imageCreds.keyEnv, imageCreds.secretEnv)
				}
			} else {
				return nil, "", fmt.Errorf("openBucket: %s and %s must be set for b2:// buckets",
					creds.keyEnv, creds.secretEnv)
			}
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
