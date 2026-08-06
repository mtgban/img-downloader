package mtgjson_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/the-muppet2/img-downloader/internal/mtgjson"
)

const allPrintingsSnippet = `{
	"meta": {"date": "2024-01-01", "version": "5.2.1"},
	"data": {
		"neo": {
			"code": "neo",
			"cards": [
				{"identifiers": {"scryfallId": "aaaa-1111"}},
				{"identifiers": {"scryfallId": "aaaa-1111"}},
				{"identifiers": {}}
			],
			"tokens": [
				{"identifiers": {"scryfallId": "tttt-2222"}}
			],
			"sealedProduct": [
				{"identifiers": {"tcgplayerProductId": "12345"}},
				{"identifiers": {}}
			]
		},
		"mid": {
			"code": "mid",
			"cards": [
				{"identifiers": {"scryfallId": "bbbb-3333"}}
			]
		}
	}
}`

func TestStreamSets(t *testing.T) {
	var sets []mtgjson.SetImages
	err := mtgjson.StreamSets(strings.NewReader(allPrintingsSnippet), func(s mtgjson.SetImages) error {
		sets = append(sets, s)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamSets: %v", err)
	}
	if len(sets) != 2 {
		t.Fatalf("got %d sets, want 2", len(sets))
	}

	byCode := make(map[string]mtgjson.SetImages)
	for _, s := range sets {
		byCode[s.Code] = s
	}

	neo, ok := byCode["NEO"]
	if !ok {
		t.Fatalf("missing set NEO (casing not uppercased?): got codes %v", codes(sets))
	}

	wantIDs := []string{"aaaa-1111", "tttt-2222"}
	gotIDs := append([]string(nil), neo.ScryfallIDs...)
	sort.Strings(gotIDs)
	sort.Strings(wantIDs)
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("NEO.ScryfallIDs = %v, want %v (dedup/skip-empty failed)", neo.ScryfallIDs, wantIDs)
	}
	for i := range gotIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Errorf("NEO.ScryfallIDs = %v, want %v", neo.ScryfallIDs, wantIDs)
			break
		}
	}

	if len(neo.Sealed) != 1 {
		t.Fatalf("NEO.Sealed = %v, want 1 entry (empty tcgplayerProductId should be skipped)", neo.Sealed)
	}
	if neo.Sealed[0].TcgplayerProductID != "12345" {
		t.Errorf("NEO.Sealed[0].TcgplayerProductID = %q, want %q", neo.Sealed[0].TcgplayerProductID, "12345")
	}

	mid, ok := byCode["MID"]
	if !ok {
		t.Fatalf("missing set MID: got codes %v", codes(sets))
	}
	if len(mid.ScryfallIDs) != 1 || mid.ScryfallIDs[0] != "bbbb-3333" {
		t.Errorf("MID.ScryfallIDs = %v, want [bbbb-3333]", mid.ScryfallIDs)
	}
	if len(mid.Sealed) != 0 {
		t.Errorf("MID.Sealed = %v, want empty (no sealedProduct key present)", mid.Sealed)
	}
}

func codes(sets []mtgjson.SetImages) []string {
	var c []string
	for _, s := range sets {
		c = append(c, s.Code)
	}
	return c
}

func TestFetch_Gzip(t *testing.T) {
	const body = `{"data":{"neo":{"code":"NEO","cards":[]}}}`

	mux := http.NewServeMux()
	mux.HandleFunc("/AllPrintings.json.gz", func(w http.ResponseWriter, r *http.Request) {
		// no Content-Encoding header: the .gz body is opaque file content,
		// not HTTP transport-level encoding (which the client would auto-decode)
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		gw.Write([]byte(body))
		gw.Close()
		w.Write(buf.Bytes())
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	rc, err := mtgjson.Fetch(context.Background(), ts.Client(), ts.URL+"/AllPrintings.json.gz")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != body {
		t.Errorf("Fetch body = %q, want %q", string(got), body)
	}
}

func TestFetch_NonGzipPassthrough(t *testing.T) {
	const body = `{"data":{}}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer ts.Close()

	rc, err := mtgjson.Fetch(context.Background(), ts.Client(), ts.URL+"/AllPrintings.json")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != body {
		t.Errorf("Fetch body = %q, want %q", string(got), body)
	}
}
