package mirror

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mtgban/simplecloud"
)

func TestRebuildBundlesOnlyRebuildsChangedSet(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	os.WriteFile(filepath.Join(filepath.FromSlash(base), "a.jpg"), []byte("aaa"), 0644)
	os.WriteFile(filepath.Join(filepath.FromSlash(base), "b.jpg"), []byte("bbb"), 0644)

	state := State{
		"card-a": {Digest: "d1", Source: "s"},
		"card-b": {Digest: "d2", Source: "s"},
	}
	want := map[string]Image{
		"card-a": {Key: "card-a", ObjectPath: "a.jpg", SetCode: "NEO"},
		"card-b": {Key: "card-b", ObjectPath: "b.jpg", SetCode: "MID"},
	}
	// NEO already matches its digest, so it is not passed as a code to rebuild.
	manifest := Manifest{"NEO": {Hash: BundleHash(map[string]string{"card-a": "d1"}), Count: 1, Bytes: 99}}

	rebuilt, err := RebuildBundles(context.Background(), &simplecloud.FileBucket{}, base, state, want, manifest, []string{"MID"})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt != 1 {
		t.Errorf("rebuilt = %d, want 1", rebuilt)
	}
	if manifest["NEO"].Bytes != 99 {
		t.Error("untouched NEO entry must survive unchanged")
	}
	wantHash := BundleHash(map[string]string{"card-b": "d2"})
	if manifest["MID"].Hash != wantHash || manifest["MID"].Count != 1 {
		t.Errorf("MID manifest entry = %+v", manifest["MID"])
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(base), "bundles", "MID-"+wantHash+".zip")); err != nil {
		t.Errorf("MID bundle not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(base), "bundles", "NEO-x.zip")); !os.IsNotExist(err) {
		t.Error("NEO must not be rebuilt")
	}
}

func TestRebuildBundlesIsolatesSetFailures(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	os.WriteFile(filepath.Join(filepath.FromSlash(base), "good.jpg"), []byte("img-good"), 0644)
	// card-bad has state but no backing image file, so its set fails.

	state := State{
		"card-good": {Digest: "d1", Source: "s"},
		"card-bad":  {Digest: "d2", Source: "s"},
	}
	want := map[string]Image{
		"card-good": {Key: "card-good", ObjectPath: "good.jpg", SetCode: "NEO"},
		"card-bad":  {Key: "card-bad", ObjectPath: "missing.jpg", SetCode: "MID"},
	}
	manifest := Manifest{}

	_, err := RebuildBundles(context.Background(), &simplecloud.FileBucket{}, base, state, want, manifest, []string{"NEO", "MID"})
	if err == nil {
		t.Fatal("expected aggregate error for the failed set")
	}
	if _, ok := manifest["NEO"]; !ok {
		t.Error("surviving set NEO missing from manifest")
	}
	if _, ok := manifest["MID"]; ok {
		t.Error("failed set MID must not enter the manifest")
	}
}
