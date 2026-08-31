package main

import (
	"context"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mtgban/img-downloader/internal/source"
	"github.com/mtgban/simplecloud"
)

func TestParseSetsEmpty(t *testing.T) {
	got := parseSets("")
	if got != nil {
		t.Errorf("parseSets(\"\") = %v, want nil", got)
	}
}

func TestParseSetsUppercases(t *testing.T) {
	got := parseSets("neo,vow, MID")
	want := map[string]bool{"NEO": true, "VOW": true, "MID": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSets = %v, want %v", got, want)
	}
}

func TestOpenBucketLocalDir(t *testing.T) {
	bucket, base, err := openBucket(context.Background(), "./tmp-mirror", imageCreds)
	if err != nil {
		t.Fatalf("openBucket local dir: %v", err)
	}
	if _, ok := bucket.(*simplecloud.FileBucket); !ok {
		t.Errorf("openBucket local dir returned %T, want *simplecloud.FileBucket", bucket)
	}
	if base != "./tmp-mirror" {
		t.Errorf("openBucket local dir base = %q, want %q", base, "./tmp-mirror")
	}
}

func TestOpenBucketRejectsWindowsAbsolutePath(t *testing.T) {
	_, _, err := openBucket(context.Background(), "C:/x", imageCreds)
	if err == nil {
		t.Fatal("openBucket(C:/x) = nil error, want rejection")
	}
}

func TestOpenBucketB2RequiresCreds(t *testing.T) {
	t.Setenv("B2_ACCESS_KEY", "")
	t.Setenv("B2_ACCESS_SECRET", "")
	_, _, err := openBucket(context.Background(), "b2://mybucket/prefix", imageCreds)
	if err == nil {
		t.Fatal("openBucket(b2://... without creds) = nil error, want error")
	}
}

func TestRequireBucketEnvMissing(t *testing.T) {
	t.Setenv("B2_BUCKET", "")
	_, err := requireBucketEnv()
	if err == nil {
		t.Fatal("requireBucketEnv() = nil error, want error")
	}
}

func TestRequireBucketEnvSet(t *testing.T) {
	t.Setenv("B2_BUCKET", "./tmp-mirror")
	got, err := requireBucketEnv()
	if err != nil {
		t.Fatalf("requireBucketEnv() error = %v", err)
	}
	if got != "./tmp-mirror" {
		t.Errorf("requireBucketEnv() = %q, want %q", got, "./tmp-mirror")
	}
}

func TestSignalContextCancelsOnSIGTERM(t *testing.T) {
	ctx := signalContext()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("signalContext did not cancel on SIGTERM")
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("IMGDL_TEST_VAR", "")
	if got := envOr("IMGDL_TEST_VAR", "fallback"); got != "fallback" {
		t.Errorf("envOr(unset) = %q, want fallback", got)
	}
	t.Setenv("IMGDL_TEST_VAR", "set")
	if got := envOr("IMGDL_TEST_VAR", "fallback"); got != "set" {
		t.Errorf("envOr(set) = %q, want set", got)
	}
}

// Magic must stay reachable without any new configuration, so the existing
// scheduled run keeps working after this change with its env untouched.
func TestNewProviderMagicNeedsNoDatastoreConfig(t *testing.T) {
	t.Setenv(datastoreEnv, "")
	p, err := newProvider(context.Background(), source.Magic)
	if err != nil {
		t.Fatalf("newProvider(magic) = %v, want success", err)
	}
	if p.Game() != source.Magic {
		t.Errorf("Game() = %q, want magic", p.Game())
	}
}

func TestNewProviderDatastoreGameRequiresConfig(t *testing.T) {
	t.Setenv(datastoreEnv, "")
	if _, err := newProvider(context.Background(), source.Lorcana); err == nil {
		t.Fatalf("newProvider(lorcana) without %s = nil error, want an error naming it", datastoreEnv)
	}
}

// The datastore document is not opened until BuildWant, so this covers the
// config plumbing only: that a local path resolves to a provider for the right
// game rather than being rejected as a bucket URL.
func TestNewProviderDatastoreGameFromLocalPath(t *testing.T) {
	t.Setenv(datastoreEnv, "./lorcana-datastore.json.xz")
	p, err := newProvider(context.Background(), source.Lorcana)
	if err != nil {
		t.Fatalf("newProvider(lorcana) = %v, want success", err)
	}
	if p.Game() != source.Lorcana {
		t.Errorf("Game() = %q, want lorcana", p.Game())
	}
	if _, ok := p.(source.SealedAware); !ok {
		t.Error("datastore provider should be sealed aware")
	}
}

// The datastore is a different bucket from the image one and wants a key of
// its own, since a B2 application key is scoped to a single bucket and the
// mirror only ever reads the datastore while it writes images.
func TestResolveCredentialsNamesBothPairsForTheDatastore(t *testing.T) {
	t.Setenv("B2_ACCESS_KEY", "")
	t.Setenv("B2_ACCESS_SECRET", "")
	t.Setenv("B2_DATASTORE_ACCESS_KEY", "")
	t.Setenv("B2_DATASTORE_ACCESS_SECRET", "")

	_, _, err := resolveCredentials(datastoreCreds)
	if err == nil {
		t.Fatal("no credentials at all = nil error, want an error")
	}
	for _, want := range []string{"B2_DATASTORE_ACCESS_KEY", "B2_ACCESS_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s, so it does not say what to set:\n%v", want, err)
		}
	}
}

// Its own pair wins where it is set, so the two buckets stay separable.
func TestResolveCredentialsPrefersTheDatastorePair(t *testing.T) {
	t.Setenv("B2_ACCESS_KEY", "image-key")
	t.Setenv("B2_ACCESS_SECRET", "image-secret")
	t.Setenv("B2_DATASTORE_ACCESS_KEY", "ds-key")
	t.Setenv("B2_DATASTORE_ACCESS_SECRET", "ds-secret")

	key, secret, err := resolveCredentials(datastoreCreds)
	if err != nil {
		t.Fatal(err)
	}
	if key != "ds-key" || secret != "ds-secret" {
		t.Errorf("resolved %q/%q, want the datastore pair", key, secret)
	}
}

// A deployment running one key across both buckets keeps working.
func TestResolveCredentialsFallsBackToTheImagePair(t *testing.T) {
	t.Setenv("B2_DATASTORE_ACCESS_KEY", "")
	t.Setenv("B2_DATASTORE_ACCESS_SECRET", "")
	t.Setenv("B2_ACCESS_KEY", "image-key")
	t.Setenv("B2_ACCESS_SECRET", "image-secret")

	key, secret, err := resolveCredentials(datastoreCreds)
	if err != nil {
		t.Fatal(err)
	}
	if key != "image-key" || secret != "image-secret" {
		t.Errorf("resolved %q/%q, want the image pair as a fallback", key, secret)
	}
}

// The image bucket has nothing to fall back to, so its error names its own
// pair and does not offer a datastore key that would not open it.
func TestResolveCredentialsImagePairHasNoFallback(t *testing.T) {
	t.Setenv("B2_ACCESS_KEY", "")
	t.Setenv("B2_ACCESS_SECRET", "")
	t.Setenv("B2_DATASTORE_ACCESS_KEY", "ds-key")
	t.Setenv("B2_DATASTORE_ACCESS_SECRET", "ds-secret")

	_, _, err := resolveCredentials(imageCreds)
	if err == nil {
		t.Fatal("no image credentials = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "B2_ACCESS_KEY") {
		t.Errorf("error does not name the image key pair:\n%v", err)
	}
	if strings.Contains(err.Error(), "DATASTORE") {
		t.Errorf("error offers a datastore key for the image bucket:\n%v", err)
	}
}

// A run is told which game it is mirroring; where that game's datastore lives
// follows from it, so there is no second thing to keep in step per game.
func TestDatastoreURLDerivesPerGame(t *testing.T) {
	t.Setenv(datastoreEnv, "")
	t.Setenv(datastoreRootEnv, "")

	for _, tt := range []struct {
		game source.Game
		want string
	}{
		{source.Lorcana, "b2://mtgban-datastore/lorcana/allCards.json"},
		{source.Riftbound, "b2://mtgban-datastore/riftbound/allCards.json"},
	} {
		if got := datastoreURL(tt.game); got != tt.want {
			t.Errorf("datastoreURL(%s) = %q, want %q", tt.game, got, tt.want)
		}
	}
}

// The root moves every game at once, for a different datastore bucket or a
// local tree of them, without naming any game individually.
func TestDatastoreURLFollowsTheRoot(t *testing.T) {
	t.Setenv(datastoreEnv, "")
	t.Setenv(datastoreRootEnv, "./datastores")
	if got, want := datastoreURL(source.Lorcana), "./datastores/lorcana/allCards.json"; got != want {
		t.Errorf("datastoreURL = %q, want %q", got, want)
	}

	// a trailing slash on the root must not double up in the path
	t.Setenv(datastoreRootEnv, "b2://other-bucket/")
	if got, want := datastoreURL(source.Riftbound), "b2://other-bucket/riftbound/allCards.json"; got != want {
		t.Errorf("datastoreURL = %q, want %q", got, want)
	}
}

// The override is for one odd document, so it wins over both the root and the
// per-game convention rather than being combined with them.
func TestDatastoreURLOverrideWinsOutright(t *testing.T) {
	t.Setenv(datastoreRootEnv, "b2://ignored")
	t.Setenv(datastoreEnv, "./lorcana-snapshot.json.xz")
	if got, want := datastoreURL(source.Lorcana), "./lorcana-snapshot.json.xz"; got != want {
		t.Errorf("datastoreURL = %q, want %q", got, want)
	}
}
