// Package scryfall resolves and streams the Scryfall default_cards bulk data.
package scryfall

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/mtgban/img-downloader/internal/web"
)

const bulkDataURL = "https://api.scryfall.com/bulk-data"

// Client calls the Scryfall API.
type Client struct {
	HTTP *http.Client
}

type bulkDataListing struct {
	Data []bulkDataEntry `json:"data"`
}

type bulkDataEntry struct {
	Type             string `json:"type"`
	JSONLDownloadURI string `json:"jsonl_download_uri"`
}

// DefaultCardsURI returns the jsonl_download_uri for the default_cards bulk entry.
func (c Client) DefaultCardsURI(ctx context.Context) (string, error) {
	resp, err := web.Get(ctx, c.HTTP, bulkDataURL)
	if err != nil {
		return "", fmt.Errorf("scryfall bulk-data: %w", err)
	}
	defer resp.Body.Close()

	var listing bulkDataListing
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return "", err
	}
	for _, entry := range listing.Data {
		if entry.Type == "default_cards" {
			return entry.JSONLDownloadURI, nil
		}
	}
	return "", errors.New("scryfall bulk-data: default_cards entry not found")
}

// BulkCard is one line of the default_cards jsonl stream.
type BulkCard struct {
	ID          string            `json:"id"`
	Set         string            `json:"set"`
	ImageStatus string            `json:"image_status"`
	ImageURIs   map[string]string `json:"image_uris"`
	CardFaces   []struct {
		ImageURIs map[string]string `json:"image_uris"`
	} `json:"card_faces"`
}

// imageVariant is the image_uris key mirrored. grid is Scryfall's own webp
// encode at the same 488x680 as the normal jpg, for a little over half the
// bytes, and carries the same ?<epoch> so reprocessing is still detectable.
const imageVariant = "grid"

// FrontImageURL returns the front image URL for the mirrored variant, or ""
// when unavailable.
func (c BulkCard) FrontImageURL() string {
	if c.ImageStatus == "missing" {
		return ""
	}
	if url, ok := c.ImageURIs[imageVariant]; ok {
		return url
	}
	if len(c.CardFaces) > 0 {
		return c.CardFaces[0].ImageURIs[imageVariant]
	}
	return ""
}

// StreamCards gunzips r and calls fn once per jsonl card line.
func StreamCards(r io.Reader, fn func(c BulkCard) error) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var card BulkCard
		if err := json.Unmarshal(line, &card); err != nil {
			return err
		}
		if err := fn(card); err != nil {
			return err
		}
	}
	return scanner.Err()
}
