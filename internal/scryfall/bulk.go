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
)

const (
	bulkDataURL = "https://api.scryfall.com/bulk-data"
	// UserAgent identifies the tool on every scryfall and image-source request.
	UserAgent = "mtgban-img-downloader/1.0 (+https://www.mtgban.com)"
)

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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bulkDataURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("scryfall bulk-data: status %d", resp.StatusCode)
	}

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

// NormalFrontURL returns the normal-size front image URL, or "" when unavailable.
func (c BulkCard) NormalFrontURL() string {
	if c.ImageStatus == "missing" {
		return ""
	}
	if url, ok := c.ImageURIs["normal"]; ok {
		return url
	}
	if len(c.CardFaces) > 0 {
		return c.CardFaces[0].ImageURIs["normal"]
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
