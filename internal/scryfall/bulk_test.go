package scryfall_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mtgban/img-downloader/internal/scryfall"
)

// rerouteTransport rewrites every request to target the given test server,
// so production code can keep its hardcoded api.scryfall.com URL.
type rerouteTransport struct {
	target     *url.URL
	gotHeaders http.Header
}

func (t *rerouteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.gotHeaders = req.Header.Clone()
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestDefaultCardsURI(t *testing.T) {
	const bulkBody = `{"data":[
		{"type":"oracle_cards","jsonl_download_uri":"https://data.scryfall.io/oracle-cards.jsonl"},
		{"type":"default_cards","jsonl_download_uri":"https://data.scryfall.io/default-cards.jsonl"}
	]}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(bulkBody))
	}))
	defer server.Close()

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	transport := &rerouteTransport{target: target}
	client := scryfall.Client{HTTP: &http.Client{Transport: transport}}

	uri, err := client.DefaultCardsURI(context.Background())
	if err != nil {
		t.Fatalf("DefaultCardsURI: %v", err)
	}
	if uri != "https://data.scryfall.io/default-cards.jsonl" {
		t.Errorf("uri = %q, want default-cards.jsonl", uri)
	}

	if got := transport.gotHeaders.Get("User-Agent"); got != "mtgban-img-downloader/1.0 (+https://www.mtgban.com)" {
		t.Errorf("User-Agent = %q, want mtgban-img-downloader/1.0 (+https://www.mtgban.com)", got)
	}
	if got := transport.gotHeaders.Get("Accept"); got != "*/*" {
		t.Errorf("Accept = %q, want */*", got)
	}
}

// gzipLines gzips a slice of JSONL lines for use as StreamCards input.
func gzipLines(t *testing.T, lines []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestStreamCards(t *testing.T) {
	lines := []string{
		`{"id":"7673784e-db4b-43a1-8d55-1bb9fc1e284f","set":"neo","image_status":"highres_scan","image_uris":{"grid":"https://cards.scryfall.io/grid/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.webp"}}`,
		`{"id":"6904ea20-e504-47da-95a0-08739fdde260","set":"soi","image_status":"highres_scan","card_faces":[{"image_uris":{"grid":"https://cards.scryfall.io/grid/front/6/9/6904ea20-e504-47da-95a0-08739fdde260.webp"}},{"image_uris":{"grid":"https://cards.scryfall.io/grid/back/6/9/6904ea20-e504-47da-95a0-08739fdde260.webp"}}]}`,
		`{"id":"cccc3333-0000-0000-0000-000000000003","set":"plst","image_status":"missing"}`,
	}
	gz := gzipLines(t, lines)

	var cards []scryfall.BulkCard
	err := scryfall.StreamCards(bytes.NewReader(gz), func(c scryfall.BulkCard) error {
		cards = append(cards, c)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamCards: %v", err)
	}
	if len(cards) != 3 {
		t.Fatalf("got %d cards, want 3", len(cards))
	}

	if id := cards[0].ID; id != "7673784e-db4b-43a1-8d55-1bb9fc1e284f" {
		t.Errorf("cards[0].ID = %q", id)
	}
	if got := cards[0].FrontImageURL(); got != "https://cards.scryfall.io/grid/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.webp" {
		t.Errorf("cards[0].FrontImageURL() = %q", got)
	}

	if got := cards[1].FrontImageURL(); got != "https://cards.scryfall.io/grid/front/6/9/6904ea20-e504-47da-95a0-08739fdde260.webp" {
		t.Errorf("cards[1].FrontImageURL() = %q, want front face URL", got)
	}

	if got := cards[2].FrontImageURL(); got != "" {
		t.Errorf("cards[2].FrontImageURL() = %q, want empty for missing image_status", got)
	}
}
