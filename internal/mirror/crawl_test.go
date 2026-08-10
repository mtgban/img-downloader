package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	img := Image{Key: "7673784e-1234", URL: srv.URL, ObjectPath: "singles/front/7/6/7673784e-1234.jpg"}

	if err := f.fetchOne(context.Background(), "test", img); err != nil {
		t.Fatal(err)
	}

	stored, err := os.ReadFile(filepath.Join(f.base, "singles", "front", "7", "6", "7673784e-1234.jpg"))
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

func TestRunStopsPromptlyOnCancelAndSavesSnapshot(t *testing.T) {
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("jpegbytes"))
	}))
	defer srvOK.Close()

	srvSlow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srvSlow.Close()

	base := filepath.ToSlash(t.TempDir())
	f := newFetcher(&simplecloud.FileBucket{}, base, State{}, log.New(io.Discard, "", 0))
	f.limit = &Limiter{Interval: 0}
	// short backoff, so a real long delay only shows up via the Retry-After header
	f.backoff = func(int) time.Duration { return 10 * time.Millisecond }

	want := map[string]Image{
		"key-a": {Key: "key-a", URL: srvOK.URL, ObjectPath: "a/key-a.jpg"},
		"key-b": {Key: "key-b", URL: srvSlow.URL, ObjectPath: "b/key-b.jpg"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	done := make(chan struct{})
	go func() {
		f.run(ctx, want, []string{"key-a", "key-b"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return promptly after ctx cancellation")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("run took %v after cancellation, want well under the 120s Retry-After delay", elapsed)
	}

	got, err := LoadState(context.Background(), &simplecloud.FileBucket{}, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["key-a"]; !ok {
		t.Error("state snapshot missing the key fetched before cancellation")
	}
}

func TestFetchAllInterruptStopsWithoutCountingFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// interrupt arrives while the second image is in flight
		if atomic.AddInt32(&hits, 1) == 2 {
			cancel()
			<-r.Context().Done()
			return
		}
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	f := testFetcher(t)
	keys := []string{"key-a", "key-b", "key-c", "key-d"}
	want := map[string]Image{}
	for _, k := range keys {
		want[k] = Image{Key: k, URL: srv.URL + "/" + k, ObjectPath: k + ".jpg"}
	}

	done, failed, err := f.run(ctx, want, keys)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0: an interrupt is not a fetch failure", failed)
	}
	if done != 1 {
		t.Errorf("done = %d, want 1 fetched before the interrupt landed", done)
	}
	// the snapshot has to survive the cancelled context or the run is not resumable
	if _, err := os.Stat(filepath.Join(f.base, "mirror-state.json")); err != nil {
		t.Errorf("state not persisted after interrupt: %v", err)
	}
}

// fetchAllKeys runs n keys against srv through a fetcher with the given breaker
// settings, returning what f.run reported.
func fetchAllKeys(t *testing.T, srvURL string, n, maxConsecutive int) (*fetcher, int, int, error) {
	t.Helper()
	f := testFetcher(t)
	f.maxConsecutive = maxConsecutive

	keys := make([]string, 0, n)
	want := map[string]Image{}
	for i := range n {
		k := fmt.Sprintf("key-%04d", i)
		keys = append(keys, k)
		want[k] = Image{Key: k, URL: srvURL + "/" + k, ObjectPath: k + ".jpg"}
	}
	done, failed, err := f.run(context.Background(), want, keys)
	return f, done, failed, err
}

func TestFetchAllAbortsWhenHostFailureRateIsHigh(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, done, failed, err := fetchAllKeys(t, srv.URL, 400, 50)
	if !errors.Is(err, ErrTooManyFailures) {
		t.Fatalf("err = %v, want ErrTooManyFailures", err)
	}
	if done != 0 {
		t.Errorf("done = %d, want 0", done)
	}
	// the point of the breaker is not walking the whole queue
	if failed > 100 {
		t.Errorf("failed = %d, want the run to abort near the %d streak limit", failed, 50)
	}
	if int(hits) > 100 {
		t.Errorf("hits = %d, breaker did not stop the requests", hits)
	}
}

func TestFetchAllToleratesScatteredFailures(t *testing.T) {
	// every tenth key 404s, the shape a healthy host with a few unpublished
	// images produces; this must not abort the run
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "0") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	f, done, failed, err := fetchAllKeys(t, srv.URL, 400, 50)
	if err != nil {
		t.Fatalf("images the source never published must not fail the run: %v", err)
	}
	if done != 360 || failed != 0 {
		t.Errorf("done=%d failed=%d, want 360 and 0: the whole queue should be walked", done, failed)
	}
	marked := 0
	for _, e := range f.state {
		if e.Missing {
			marked++
		}
	}
	if marked != 40 {
		t.Errorf("marked %d entries not-published, want 40", marked)
	}
}

func TestNotPublishedIsRecordedAndNotRefetched(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if strings.HasSuffix(r.URL.Path, "gone") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	f := testFetcher(t)
	want := map[string]Image{
		"key-ok":   {Key: "key-ok", URL: srv.URL + "/ok", ObjectPath: "a/key-ok.jpg", SetCode: "NEO"},
		"key-gone": {Key: "key-gone", URL: srv.URL + "/gone", ObjectPath: "b/key-gone.jpg", SetCode: "NEO"},
	}
	keys := []string{"key-gone", "key-ok"}

	done, failed, err := f.run(context.Background(), want, keys)
	if err != nil {
		t.Fatalf("a not-published image must not fail the run: %v", err)
	}
	if done != 1 || failed != 0 {
		t.Errorf("done=%d failed=%d, want 1 and 0", done, failed)
	}
	entry, ok := f.state["key-gone"]
	if !ok || !entry.Missing {
		t.Fatalf("key-gone state = %+v, want a Missing marker", entry)
	}
	if entry.Digest != "" {
		t.Errorf("a missing entry must carry no digest, got %q", entry.Digest)
	}

	// the marker must keep the next run from asking again
	if got := NeedFetch(f.state, want); len(got) != 0 {
		t.Errorf("NeedFetch = %v, want nothing re-queued", got)
	}
	// and must stay out of the bundle
	if d := SetDigests(f.state, want); len(d["NEO"]) != 1 {
		t.Errorf("NEO digests = %v, want only the fetched key", d["NEO"])
	}

	before := atomic.LoadInt32(&hits)
	if _, _, err := f.run(context.Background(), want, NeedFetch(f.state, want)); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != before {
		t.Errorf("second run made %d more requests, want 0", atomic.LoadInt32(&hits)-before)
	}
}

func TestTripTakesBackMarkersFromItsStreak(t *testing.T) {
	// a host 404ing everything is broken, not authoritative: nothing it said
	// during the streak may be recorded as permanently missing
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	f := testFetcher(t)
	f.maxConsecutive = 50
	keys := make([]string, 0, 400)
	want := map[string]Image{}
	for i := range 400 {
		k := fmt.Sprintf("key-%04d", i)
		keys = append(keys, k)
		want[k] = Image{Key: k, URL: srv.URL + "/" + k, ObjectPath: k + ".jpg"}
	}

	if _, _, err := f.run(context.Background(), want, keys); !errors.Is(err, ErrTooManyFailures) {
		t.Fatalf("err = %v, want ErrTooManyFailures", err)
	}
	for k, e := range f.state {
		if e.Missing {
			t.Fatalf("%s was retired during a tripped streak: %+v", k, e)
		}
	}
	if len(NeedFetch(f.state, want)) != len(want) {
		t.Error("every key must remain queued after an aborted run")
	}
}

func runWithProgress(t *testing.T, interval time.Duration) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	f := testFetcher(t)
	f.log = log.New(&buf, "", 0)
	f.progressInterval = interval

	keys := make([]string, 0, 25)
	want := map[string]Image{}
	for i := range 25 {
		k := fmt.Sprintf("key-%04d", i)
		keys = append(keys, k)
		want[k] = Image{Key: k, URL: srv.URL + "/" + k, ObjectPath: k + ".jpg"}
	}
	if _, _, err := f.run(context.Background(), want, keys); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestFetchAllLogsProgressPeriodically(t *testing.T) {
	// an interval this short elapses between fetches, so the crawl reports
	// repeatedly rather than only in the final summary
	out := runWithProgress(t, time.Nanosecond)
	if n := strings.Count(out, "/25 ("); n < 2 {
		t.Errorf("progress lines = %d, want repeated reporting, in:\n%s", n, out)
	}
}

func TestFetchAllStaysQuietWithinTheInterval(t *testing.T) {
	// nothing but the closing summary should appear before the first interval
	out := runWithProgress(t, time.Hour)
	if n := strings.Count(out, "/25 ("); n != 0 {
		t.Errorf("progress lines = %d, want none inside the interval, in:\n%s", n, out)
	}
	if !strings.Contains(out, "fetched 25 images") {
		t.Errorf("missing the final summary, got:\n%s", out)
	}
}
