package mirror

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mtgban/simplecloud"
)

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

func TestRunEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("imgbytes-" + r.URL.RawQuery))
	}))
	defer srv.Close()

	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}
	want := map[string]Image{
		"card-a": {Key: "card-a", URL: srv.URL + "/a.jpg?t=1", ObjectPath: "a/card-a.jpg", SetCode: "NEO"},
		"card-b": {Key: "card-b", URL: srv.URL + "/b.jpg?t=1", ObjectPath: "b/card-b.jpg", SetCode: "MID"},
	}

	res, err := Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: want, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 2 || res.Fetched != 2 || res.FetchFailed != 0 || res.BundlesRebuilt != 2 {
		t.Errorf("first run res = %+v", res)
	}
	manifest, err := LoadManifest(context.Background(), bucket, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 2 {
		t.Errorf("manifest = %+v, want 2 sets", manifest)
	}

	// second run against the same want: nothing pending, nothing rebuilt.
	res2, err := Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: want, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Pending != 0 || res2.Fetched != 0 || res2.FetchFailed != 0 || res2.BundlesRebuilt != 0 {
		t.Errorf("second run res = %+v, want all zero", res2)
	}

	// bump the source URL for card-a only: refetch and rebuild just NEO.
	want2 := map[string]Image{
		"card-a": {Key: "card-a", URL: srv.URL + "/a.jpg?t=2", ObjectPath: "a/card-a.jpg", SetCode: "NEO"},
		"card-b": want["card-b"],
	}
	res3, err := Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: want2, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Pending != 1 || res3.Fetched != 1 || res3.FetchFailed != 0 || res3.BundlesRebuilt != 1 {
		t.Errorf("third run res = %+v, want pending=1 fetched=1 rebuilt=1", res3)
	}
}

func TestRunSkipSealedDoesNotFetchButKeepsBundle(t *testing.T) {
	sealedHits := 0
	sealedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sealedHits++
		w.Write([]byte("sealed-bytes"))
	}))
	defer sealedSrv.Close()

	cardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("card-bytes"))
	}))
	defer cardSrv.Close()

	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}
	sealedOnly := map[string]Image{
		"p-NEO-111": {Key: "p-NEO-111", URL: sealedSrv.URL + "/111.jpg", ObjectPath: "sealed/NEO/111.jpg", SetCode: "NEO"},
	}

	// prior run actually fetches the sealed image, establishing its bundle membership
	if _, err := Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: sealedOnly, Log: discardLog()}); err != nil {
		t.Fatal(err)
	}
	if sealedHits != 1 {
		t.Fatalf("setup: sealedHits = %d, want 1", sealedHits)
	}

	// this run adds a new single and passes SkipSealed with the sealed key still wanted
	want := map[string]Image{
		"card-a":    {Key: "card-a", URL: cardSrv.URL + "/a.jpg", ObjectPath: "a/card-a.jpg", SetCode: "NEO"},
		"p-NEO-111": sealedOnly["p-NEO-111"],
	}
	res, err := Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: want, SkipSealed: true, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if sealedHits != 1 {
		t.Errorf("skip-sealed still fetched the TCGplayer URL, sealedHits = %d", sealedHits)
	}
	if res.Fetched != 1 {
		t.Errorf("res.Fetched = %d, want 1 (card-a only)", res.Fetched)
	}

	finalState, err := LoadState(context.Background(), bucket, base)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(context.Background(), bucket, base)
	if err != nil {
		t.Fatal(err)
	}
	wantDigests := map[string]string{"card-a": finalState["card-a"].Digest, "p-NEO-111": finalState["p-NEO-111"].Digest}
	if manifest["NEO"].Hash != BundleHash(wantDigests) {
		t.Errorf("manifest NEO = %+v, sealed entry missing from bundle digests", manifest["NEO"])
	}
}

func TestRunDryRunPlansWithoutWriting(t *testing.T) {
	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}
	want := map[string]Image{
		"card-a": {Key: "card-a", URL: "http://mirror.invalid/a.jpg", ObjectPath: "a/card-a.jpg", SetCode: "NEO"},
	}

	res, err := Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: want, DryRun: true, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 1 || res.Fetched != 0 || res.BundlesRebuilt != 0 {
		t.Errorf("dry run res = %+v, want pending=1 and no writes", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(base), "mirror-state.json")); !os.IsNotExist(err) {
		t.Error("dry run must not write state")
	}
}

func TestRunInterruptSkipsBundlesButPersistsState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 2 {
			cancel()
			<-r.Context().Done()
			return
		}
		w.Write([]byte("imgbytes"))
	}))
	defer srv.Close()

	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}
	want := map[string]Image{}
	for _, k := range []string{"card-a", "card-b", "card-c"} {
		want[k] = Image{Key: k, URL: srv.URL + "/" + k, ObjectPath: k + ".jpg", SetCode: "NEO"}
	}

	res, err := Run(ctx, Opts{Bucket: bucket, Base: base, Want: want, Log: discardLog()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.BundlesRebuilt != 0 {
		t.Errorf("BundlesRebuilt = %d, want 0: an interrupted run must not bundle a half-fetched set", res.BundlesRebuilt)
	}

	// partial progress must land so the next run resumes rather than refetching
	state, err := LoadState(context.Background(), bucket, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(state) != 1 {
		t.Errorf("state has %d entries, want the 1 image fetched before the interrupt", len(state))
	}
}

func TestRunRefetchSealedRestoresThemAtTheCurrentPath(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("sealed-bytes"))
	}))
	defer srv.Close()

	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}
	want := map[string]Image{
		"p-NEO-111": {Key: "p-NEO-111", URL: srv.URL + "/111.jpg", ObjectPath: SealedObjectPath("NEO", "111"), SetCode: "NEO"},
		"card-a":    {Key: "card-a", URL: srv.URL + "/a.jpg", ObjectPath: "a/card-a.jpg", SetCode: "NEO"},
	}

	if _, err := Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: want, Log: discardLog()}); err != nil {
		t.Fatal(err)
	}
	first := atomic.LoadInt32(&hits)

	// without the flag a sealed URL never changes, so nothing is re-queued
	res, err := Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: want, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 0 || atomic.LoadInt32(&hits) != first {
		t.Fatalf("plain rerun refetched: pending=%d hits=%d", res.Pending, atomic.LoadInt32(&hits))
	}

	// with it, sealed alone comes back, which is what applies a path change
	res, err = Run(context.Background(), Opts{Bucket: bucket, Base: base, Want: want, RefetchSealed: true, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 1 || res.Fetched != 1 {
		t.Errorf("res = %+v, want exactly the one sealed image re-fetched", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(base), "sealed", "NEO", "111.jpg")); err != nil {
		t.Errorf("sealed image not stored at the current path: %v", err)
	}
}
