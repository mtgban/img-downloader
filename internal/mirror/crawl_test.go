package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mtgban/simplecloud"
)

func testFetcher(t *testing.T) *fetcher {
	t.Helper()
	base := filepath.ToSlash(t.TempDir())
	f := newFetcher(&simplecloud.FileBucket{}, base, State{}, log.New(io.Discard, "", 0))
	f.limit = &Limiter{Interval: 0}
	f.backoff = func(int) time.Duration { return 0 }
	return f
}

func TestDownloadRetriesOn429(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	f := testFetcher(t)
	data, err := f.download(context.Background(), "test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "jpegbytes" {
		t.Errorf("body = %q", data)
	}
	if hits != 3 {
		t.Errorf("attempts = %d, want 3", hits)
	}
}

func TestDownloadFailsFastOn404(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	f := testFetcher(t)
	if _, err := f.download(context.Background(), "test", srv.URL); err == nil {
		t.Fatal("expected error on 404")
	}
	if hits != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 404)", hits)
	}
}

func TestDownloadGivesUpAfterRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := testFetcher(t)
	if _, err := f.download(context.Background(), "test", srv.URL); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestFetchOneWritesShardedPathAndState(t *testing.T) {
	payload := []byte("jpegbytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	f := testFetcher(t)
	img := Image{Key: "7673784e-1234", URL: srv.URL, ObjectPath: "normal/front/7/6/7673784e-1234.jpg"}

	if err := f.fetchOne(context.Background(), "test", img); err != nil {
		t.Fatal(err)
	}

	stored, err := os.ReadFile(filepath.Join(f.base, "normal", "front", "7", "6", "7673784e-1234.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(payload) {
		t.Error("stored bytes differ from source")
	}

	entry := f.state["7673784e-1234"]
	sum := sha256.Sum256(payload)
	if entry.Digest != hex.EncodeToString(sum[:]) {
		t.Errorf("digest = %q", entry.Digest)
	}
	if entry.Source != srv.URL {
		t.Errorf("source = %q, want %q", entry.Source, srv.URL)
	}
	if entry.FetchedAt == "" {
		t.Error("fetchedAt not set")
	}
}

func TestFetchAllReturnsErrorAndKeepsGoingAfterFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	f := testFetcher(t)
	want := map[string]Image{
		"key-fail": {Key: "key-fail", URL: srv.URL + "/fail", ObjectPath: "a/key-fail.jpg"},
		"key-ok":   {Key: "key-ok", URL: srv.URL + "/ok", ObjectPath: "b/key-ok.jpg"},
	}
	done, failed, err := f.run(context.Background(), want, []string{"key-fail", "key-ok"})
	if err == nil {
		t.Fatal("expected non-nil error when a fetch permanently fails")
	}
	if done != 1 || failed != 1 {
		t.Errorf("done=%d failed=%d, want 1 and 1", done, failed)
	}
	if _, ok := f.state["key-ok"]; !ok {
		t.Error("second key was not fetched after the first failed")
	}
}

func TestFetchAllSnapshotsStateAfterRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	f := testFetcher(t)
	want := map[string]Image{
		"key-ok": {Key: "key-ok", URL: srv.URL, ObjectPath: "a/key-ok.jpg"},
	}
	if _, _, err := f.run(context.Background(), want, []string{"key-ok"}); err != nil {
		t.Fatal(err)
	}

	got, err := LoadState(context.Background(), &simplecloud.FileBucket{}, f.base)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["key-ok"]; !ok {
		t.Error("state snapshot missing the fetched key")
	}
}

func TestFetchAllWiring(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	base := filepath.ToSlash(t.TempDir())
	want := map[string]Image{
		"key-ok": {Key: "key-ok", URL: srv.URL, ObjectPath: "a/key-ok.jpg"},
	}
	fetched, failed, err := FetchAll(context.Background(), &simplecloud.FileBucket{}, base, State{}, want, []string{"key-ok"}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if fetched != 1 || failed != 0 {
		t.Errorf("fetched=%d failed=%d, want 1 and 0", fetched, failed)
	}
	if _, err := os.Stat(filepath.Join(base, "a", "key-ok.jpg")); err != nil {
		t.Errorf("object not written: %v", err)
	}
}
