package mirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

	rebuilt, err := RebuildBundles(context.Background(), &simplecloud.FileBucket{}, base, state, want, manifest, []string{"MID"}, discardLog())
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

	_, err := RebuildBundles(context.Background(), &simplecloud.FileBucket{}, base, state, want, manifest, []string{"NEO", "MID"}, discardLog())
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

func TestRebuildBundlesSnapshotsManifestAsItGoes(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}

	// one image per set so every rebuild succeeds cheaply
	state := State{}
	want := map[string]Image{}
	var codes []string
	for i := range bundleSaveEvery + 5 {
		code := fmt.Sprintf("S%02d", i)
		key := "card-" + code
		codes = append(codes, code)
		state[key] = StateEntry{Digest: "d" + code}
		want[key] = Image{Key: key, ObjectPath: key + ".jpg", SetCode: code}
		if err := os.WriteFile(filepath.Join(filepath.FromSlash(base), key+".jpg"), []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	manifest := Manifest{}
	if _, err := RebuildBundles(context.Background(), bucket, base, state, want, manifest, codes, discardLog()); err != nil {
		t.Fatal(err)
	}

	// the on-disk manifest must have been written mid-loop, not only by the
	// caller afterwards, or a killed run loses every bundle it built
	saved, err := LoadManifest(context.Background(), bucket, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != bundleSaveEvery {
		t.Errorf("snapshotted manifest has %d sets, want %d from the mid-loop save", len(saved), bundleSaveEvery)
	}
}

func TestRebuildBundlesStopsOnCancellation(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	codes := []string{"NEO", "MID", "ARB"}
	_, err := RebuildBundles(ctx, bucket, base, State{}, map[string]Image{}, Manifest{}, codes, discardLog())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled rather than a per-set failure list", err)
	}
}

// TestRebuildBundlesParallelProducesConsistentManifest rebuilds more sets than
// there are workers, each with several images, and checks every manifest
// entry and zip against the values a serial rebuild would produce.
func TestRebuildBundlesParallelProducesConsistentManifest(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())

	state := State{}
	want := map[string]Image{}
	wantHash := map[string]string{}
	var codes []string
	const sets = bundleWorkers*3 + 2
	const imagesPerSet = 3
	for i := range sets {
		code := fmt.Sprintf("SET%02d", i)
		codes = append(codes, code)
		digests := map[string]string{}
		for j := range imagesPerSet {
			key := fmt.Sprintf("card-%s-%d", code, j)
			digest := fmt.Sprintf("digest-%s-%d", code, j)
			state[key] = StateEntry{Digest: digest}
			want[key] = Image{Key: key, ObjectPath: key + ".jpg", SetCode: code}
			digests[key] = digest
			data := []byte(fmt.Sprintf("data-%s-%d", code, j))
			if err := os.WriteFile(filepath.Join(filepath.FromSlash(base), key+".jpg"), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		wantHash[code] = BundleHash(digests)
	}

	manifest := Manifest{}
	rebuilt, err := RebuildBundles(context.Background(), &simplecloud.FileBucket{}, base, state, want, manifest, codes, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt != sets {
		t.Errorf("rebuilt = %d, want %d", rebuilt, sets)
	}
	if len(manifest) != sets {
		t.Fatalf("manifest has %d entries, want %d", len(manifest), sets)
	}
	for _, code := range codes {
		info, ok := manifest[code]
		if !ok {
			t.Errorf("%s missing from manifest", code)
			continue
		}
		if info.Hash != wantHash[code] {
			t.Errorf("%s hash = %s, want %s", code, info.Hash, wantHash[code])
		}
		if info.Count != imagesPerSet {
			t.Errorf("%s count = %d, want %d", code, info.Count, imagesPerSet)
		}
		if _, err := os.Stat(filepath.Join(filepath.FromSlash(base), "bundles", code+"-"+info.Hash+".zip")); err != nil {
			t.Errorf("%s bundle not written: %v", code, err)
		}
	}
}

// gateAfterReads lets the first limit reads through to the wrapped bucket,
// then blocks any further read until the run's context is cancelled, so no
// more than limit sets can ever complete successfully.
type gateAfterReads struct {
	simplecloud.ReadWriter
	limit int32
	reads int32
}

func (g *gateAfterReads) NewReader(ctx context.Context, path string) (io.ReadCloser, error) {
	if atomic.AddInt32(&g.reads, 1) > g.limit {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return g.ReadWriter.NewReader(ctx, path)
}

// cancelOnMatch cancels the run the first time a logged line contains match,
// used to cancel right as the run observes its own progress snapshot rather
// than at an arbitrary, racy point in the middle of the worker pool.
type cancelOnMatch struct {
	match  string
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelOnMatch) Write(p []byte) (int, error) {
	if strings.Contains(string(p), c.match) {
		c.once.Do(c.cancel)
	}
	return len(p), nil
}

// TestRebuildBundlesCancelMidRunSnapshotsCompletedWork gates every read past
// the bundleSaveEvery'th so it blocks rather than fails, meaning only
// successes can land before the run is cancelled; cancellation itself is
// triggered off the progress log line for that very snapshot. That removes
// the two usual sources of flakiness (workers racing failures into the
// completion count, and cancelling before or after the snapshot fires) and
// makes the outcome exact: the run must stop with ctx.Canceled having
// rebuilt exactly bundleSaveEvery sets, all of which reached the manifest
// snapshot saved to the bucket.
func TestRebuildBundlesCancelMidRunSnapshotsCompletedWork(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())

	state := State{}
	want := map[string]Image{}
	var codes []string
	total := bundleSaveEvery + 5
	for i := range total {
		code := fmt.Sprintf("S%02d", i)
		key := "card-" + code
		codes = append(codes, code)
		state[key] = StateEntry{Digest: "d" + code}
		want[key] = Image{Key: key, ObjectPath: key + ".jpg", SetCode: code}
		if err := os.WriteFile(filepath.Join(filepath.FromSlash(base), key+".jpg"), []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bucket := &gateAfterReads{ReadWriter: &simplecloud.FileBucket{}, limit: int32(bundleSaveEvery)}
	logger := log.New(&cancelOnMatch{match: fmt.Sprintf("rebuilt %d/", bundleSaveEvery), cancel: cancel}, "", 0)

	manifest := Manifest{}
	rebuilt, err := RebuildBundles(ctx, bucket, base, state, want, manifest, codes, logger)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if rebuilt != bundleSaveEvery {
		t.Errorf("rebuilt = %d, want exactly %d (only the sets let through before cancellation)", rebuilt, bundleSaveEvery)
	}
	if len(manifest) != bundleSaveEvery {
		t.Errorf("live manifest has %d entries, want %d", len(manifest), bundleSaveEvery)
	}

	saved, err := LoadManifest(context.Background(), &simplecloud.FileBucket{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != bundleSaveEvery {
		t.Errorf("snapshotted manifest has %d sets, want the %d completed before cancellation", len(saved), bundleSaveEvery)
	}
}
