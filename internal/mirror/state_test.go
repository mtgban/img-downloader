package mirror_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mtgban/simplecloud"
	"github.com/mtgban/img-downloader/internal/mirror"
)

// errBucket fails every read with a fixed non-not-found error.
type errBucket struct{ err error }

func (b errBucket) NewReader(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, b.err
}

func TestSaveLoadStateRoundTrip(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	want := mirror.State{
		"7673784e-1234": {Digest: "abc123", FetchedAt: "2026-08-06T00:00:00Z", Source: "https://cards.scryfall.io/normal/front/7/6/7673784e-1234.jpg"},
	}
	if err := mirror.SaveState(context.Background(), &simplecloud.FileBucket{}, base, want); err != nil {
		t.Fatal(err)
	}
	got, err := mirror.LoadState(context.Background(), &simplecloud.FileBucket{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got["7673784e-1234"] != want["7673784e-1234"] {
		t.Errorf("state = %v, want %v", got, want)
	}
}

func TestSaveLoadManifestRoundTrip(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	want := mirror.Manifest{
		"MH3": {Hash: "deadbeef", Count: 5, Bytes: 1024},
	}
	if err := mirror.SaveManifest(context.Background(), &simplecloud.FileBucket{}, base, want); err != nil {
		t.Fatal(err)
	}
	got, err := mirror.LoadManifest(context.Background(), &simplecloud.FileBucket{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got["MH3"] != want["MH3"] {
		t.Errorf("manifest = %v, want %v", got, want)
	}
}

func TestLoadStateMissingFileStartsEmpty(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	state, err := mirror.LoadState(context.Background(), &simplecloud.FileBucket{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || len(state) != 0 {
		t.Errorf("state = %v, want empty non-nil", state)
	}
}

func TestLoadManifestMissingFileStartsEmpty(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	manifest, err := mirror.LoadManifest(context.Background(), &simplecloud.FileBucket{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if manifest == nil || len(manifest) != 0 {
		t.Errorf("manifest = %v, want empty non-nil", manifest)
	}
}

func TestLoadStateTransientErrorFails(t *testing.T) {
	if _, err := mirror.LoadState(context.Background(), errBucket{errors.New("auth failed")}, "base"); err == nil {
		t.Fatal("transient error must not silently start empty")
	}
}

func TestLoadStateCorruptFileFails(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	os.WriteFile(filepath.Join(base, "mirror-state.json"), []byte("{not json"), 0644)
	if _, err := mirror.LoadState(context.Background(), &simplecloud.FileBucket{}, base); err == nil {
		t.Fatal("expected error on corrupt state")
	}
}

func TestLoadManifestCorruptFileFails(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	os.WriteFile(filepath.Join(base, "images-manifest.json"), []byte("{not json"), 0644)
	if _, err := mirror.LoadManifest(context.Background(), &simplecloud.FileBucket{}, base); err == nil {
		t.Fatal("expected error on corrupt manifest")
	}
}
