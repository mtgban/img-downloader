package main

import (
	"context"
	"reflect"
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
	bucket, base, err := openBucket(context.Background(), "./tmp-mirror")
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
	_, _, err := openBucket(context.Background(), "C:/x")
	if err == nil {
		t.Fatal("openBucket(C:/x) = nil error, want rejection")
	}
}

func TestOpenBucketB2RequiresCreds(t *testing.T) {
	t.Setenv("B2_ACCESS_KEY", "")
	t.Setenv("B2_ACCESS_SECRET", "")
	_, _, err := openBucket(context.Background(), "b2://mybucket/prefix")
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
