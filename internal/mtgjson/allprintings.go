// Package mtgjson streams the MTGJSON AllPrintings.json set data without
// decoding the whole (potentially ~600MB) document into memory at once.
package mtgjson

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const userAgent = "mtgban-img-downloader/1.0 (+https://www.mtgban.com)"

// SetImages is the image-relevant slice of one AllPrintings set entry.
type SetImages struct {
	Code        string
	ScryfallIDs []string
	Sealed      []SealedRef
}

// SealedRef identifies one sealed product image to mirror.
type SealedRef struct {
	TcgplayerProductID string
}

type slimIdentifiers struct {
	ScryfallID string `json:"scryfallId"`
}

type slimCard struct {
	Identifiers slimIdentifiers `json:"identifiers"`
}

type slimSealedIdentifiers struct {
	TcgplayerProductID string `json:"tcgplayerProductId"`
}

type slimSealedProduct struct {
	Identifiers slimSealedIdentifiers `json:"identifiers"`
}

type slimSet struct {
	Code          string              `json:"code"`
	Cards         []slimCard          `json:"cards"`
	Tokens        []slimCard          `json:"tokens"`
	SealedProduct []slimSealedProduct `json:"sealedProduct"`
}

// StreamSets token-walks an AllPrintings.json document, calling fn once per
// set without buffering the full "data" object in memory.
func StreamSets(r io.Reader, fn func(s SetImages) error) error {
	dec := json.NewDecoder(r)

	if err := advanceToDataObject(dec); err != nil {
		return err
	}

	for dec.More() {
		// consume the set-code key; the slim struct's own "code" field is used instead
		if _, err := dec.Token(); err != nil {
			return err
		}

		var slim slimSet
		if err := dec.Decode(&slim); err != nil {
			return err
		}

		if err := fn(toSetImages(slim)); err != nil {
			return err
		}
	}

	// consume the closing brace of the "data" object
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}

// advanceToDataObject walks top-level keys, skipping any that aren't "data",
// leaving the decoder positioned just inside the "data" object.
func advanceToDataObject(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("mtgjson: expected top-level object, got %v", tok)
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("mtgjson: expected string key, got %v", keyTok)
		}
		if key == "data" {
			dataTok, err := dec.Token()
			if err != nil {
				return err
			}
			if d, ok := dataTok.(json.Delim); !ok || d != '{' {
				return fmt.Errorf("mtgjson: expected \"data\" object, got %v", dataTok)
			}
			return nil
		}

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
	}
	return fmt.Errorf("mtgjson: no \"data\" key found")
}

func toSetImages(s slimSet) SetImages {
	seen := make(map[string]bool)
	var ids []string
	collect := func(cards []slimCard) {
		for _, c := range cards {
			id := c.Identifiers.ScryfallID
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	collect(s.Cards)
	collect(s.Tokens)

	var sealed []SealedRef
	for _, sp := range s.SealedProduct {
		id := sp.Identifiers.TcgplayerProductID
		if id == "" {
			continue
		}
		sealed = append(sealed, SealedRef{TcgplayerProductID: id})
	}

	return SetImages{
		Code:        strings.ToUpper(s.Code),
		ScryfallIDs: ids,
		Sealed:      sealed,
	}
}

// Fetch GETs url, gunzipping the body when the URL ends in ".gz".
func Fetch(ctx context.Context, httpClient *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	client := httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("mtgjson: fetch %s: status %d", url, resp.StatusCode)
	}

	if !strings.HasSuffix(url, ".gz") {
		return resp.Body, nil
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	return &gzipReadCloser{gz: gz, body: resp.Body}, nil
}

// gzipReadCloser closes both the gzip stream and the underlying HTTP body.
type gzipReadCloser struct {
	gz   *gzip.Reader
	body io.ReadCloser
}

func (g *gzipReadCloser) Read(p []byte) (int, error) {
	return g.gz.Read(p)
}

func (g *gzipReadCloser) Close() error {
	err := g.gz.Close()
	if cerr := g.body.Close(); err == nil {
		err = cerr
	}
	return err
}
