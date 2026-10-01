// Package web builds the HTTP requests the tool makes, so every source sees
// the same identification.
package web

import (
	"context"
	"fmt"
	"net/http"
)

// UserAgent identifies the tool on every request it makes.
const UserAgent = "mtgban-img-downloader/1.0 (+https://www.mtgban.com)"

// NewRequest returns a GET for url carrying the tool's headers.
func NewRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")
	return req, nil
}

// Get fetches url with client, nil meaning http.DefaultClient. Only a 200 is
// returned; any other status is an error naming url, with the body closed.
func Get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := NewRequest(ctx, url)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return resp, nil
}
