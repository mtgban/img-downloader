package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/source"
)

const (
	runCardID   = "7673784e-db4b-43a1-8d55-1bb9fc1e284f"
	runSetCode  = "TST"
	runSealedID = "12345"
)

// magicSources stands in for every host a Magic run talks to. Requests are
// routed by path, whatever host they were addressed to.
type magicSources struct {
	srv        *httptest.Server
	imageHits  atomic.Int32
	sealedHits atomic.Int32
}

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func cardPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 6))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(1, 1, color.RGBA{R: 0xff, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newMagicSources serves one set holding one single and one sealed product
// TCGplayer has no image for, and routes the default transport to it.
func newMagicSources(t *testing.T) *magicSources {
	t.Helper()
	allPrintings := gz(t, `{"meta":{"version":"test"},"data":{"`+runSetCode+`":{
		"code":"`+strings.ToLower(runSetCode)+`",
		"cards":[{"identifiers":{"scryfallId":"`+runCardID+`"}}],
		"tokens":[],
		"sealedProduct":[{"identifiers":{"tcgplayerProductId":"`+runSealedID+`"}}]}}}`)
	cards := gz(t, `{"id":"`+runCardID+`","set":"tst","image_status":"highres_scan","image_uris":{"grid":"https://cards.scryfall.io/grid/front/7/6/`+runCardID+`.png?1700000000"}}`+"\n")
	image := cardPNG(t)

	s := &magicSources{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/bulk-data":
			w.Write([]byte(`{"data":[{"type":"default_cards","jsonl_download_uri":"https://data.scryfall.io/default-cards.jsonl.gz"}]}`))
		case r.URL.Path == "/default-cards.jsonl.gz":
			w.Write(cards)
		case r.URL.Path == "/api/v5/AllPrintings.json.gz":
			w.Write(allPrintings)
		case strings.HasPrefix(r.URL.Path, "/grid/front/"):
			s.imageHits.Add(1)
			w.Write(image)
		case r.URL.Path == "/"+runSealedID+".jpg":
			s.sealedHits.Add(1)
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)

	target, err := url.Parse(s.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host = target.Scheme, target.Host
		return orig.RoundTrip(req)
	})
	t.Cleanup(func() { http.DefaultTransport = orig })
	return s
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// A Magic run against an empty base stores the single as webp, records the
// sealed product as not published, builds the set's bundle, and claims the
// base; a second run finds nothing to fetch.
func TestRunMagicEndToEnd(t *testing.T) {
	src := newMagicSources(t)
	base := filepath.ToSlash(t.TempDir())
	cfg := opts{game: source.Magic, bucket: base}

	if err := run(context.Background(), cfg); err != nil {
		t.Fatalf("first run: %v", err)
	}

	objectPath, err := mirror.SingleObjectPath(runCardID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(base, objectPath))
	if err != nil {
		t.Fatalf("single not stored: %v", err)
	}
	if !bytes.HasPrefix(stored, []byte("RIFF")) || string(stored[8:12]) != "WEBP" {
		t.Errorf("stored single is not webp")
	}

	var state mirror.State
	readJSON(t, filepath.Join(base, "mirror-state.json"), &state)
	if got := state[runCardID]; got.Digest == "" || got.ObjectPath != objectPath {
		t.Errorf("single state = %+v, want a digest and objectPath %s", got, objectPath)
	}
	if got := state[mirror.SealedKey(runSetCode, runSealedID)]; !got.Missing {
		t.Errorf("sealed state = %+v, want it marked missing", got)
	}

	var manifest mirror.Manifest
	readJSON(t, filepath.Join(base, "images-manifest.json"), &manifest)
	info, ok := manifest[runSetCode]
	if !ok || info.Count != 1 {
		t.Fatalf("manifest = %+v, want %s with one image", manifest, runSetCode)
	}
	if _, err := os.Stat(filepath.Join(base, mirror.BundleObjectPath(runSetCode, info.Hash))); err != nil {
		t.Errorf("bundle not stored: %v", err)
	}

	var marker struct{ Game string }
	readJSON(t, filepath.Join(base, "mirror-game.json"), &marker)
	if marker.Game != string(source.Magic) {
		t.Errorf("mirror-game.json names %q, want magic", marker.Game)
	}

	if err := run(context.Background(), cfg); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n, m := src.imageHits.Load(), src.sealedHits.Load(); n != 1 || m != 1 {
		t.Errorf("image fetched %d times and sealed asked %d times over two runs, want once each", n, m)
	}
}

// A dry run reads the sources but writes nothing, not even the game claim.
func TestRunMagicDryRunWritesNothing(t *testing.T) {
	src := newMagicSources(t)
	base := filepath.ToSlash(t.TempDir())

	if err := run(context.Background(), opts{game: source.Magic, bucket: base, dryRun: true}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run wrote %d entries to the base, want none", len(entries))
	}
	if n := src.imageHits.Load(); n != 0 {
		t.Errorf("dry run fetched %d images", n)
	}
}

// A base another game claimed is refused before any source is read.
func TestRunRefusesAnotherGamesBase(t *testing.T) {
	src := newMagicSources(t)
	base := filepath.ToSlash(t.TempDir())
	if err := os.WriteFile(filepath.Join(base, "mirror-game.json"), []byte(`{"game":"lorcana"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := run(context.Background(), opts{game: source.Magic, bucket: base})
	if err == nil || !strings.Contains(err.Error(), "lorcana") {
		t.Fatalf("run = %v, want a refusal naming lorcana", err)
	}
	if n := src.imageHits.Load(); n != 0 {
		t.Errorf("refused run fetched %d images", n)
	}
}
