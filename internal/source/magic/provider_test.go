package magic_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mtgban/img-downloader/internal/source/magic"
)

// sourceServer routes every request to handler by path, whatever host it was
// addressed to, and returns a provider talking to it.
func sourceServer(t *testing.T, handler http.HandlerFunc) *magic.Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: rerouteFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(req)
	})}
	return &magic.Provider{HTTP: client, Log: log.New(io.Discard, "", 0)}
}

type rerouteFunc func(*http.Request) (*http.Response, error)

func (f rerouteFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const bulkListing = `{"data":[{"type":"default_cards","jsonl_download_uri":"https://data.scryfall.io/default-cards.jsonl.gz"}]}`

func TestProviderBuildWantFailsOnBulkDownloadStatus(t *testing.T) {
	p := sourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bulk-data" {
			_, err := io.WriteString(w, bulkListing)
			if err != nil {
				t.Error(err)
			}
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := p.BuildWant(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "scryfall bulk download") || !strings.Contains(err.Error(), "502") {
		t.Fatalf("BuildWant = %v, want a scryfall bulk download error naming the 502", err)
	}
}

// truncatingBody serves data, then cancels the run and ends the stream as a
// dropped connection does: mid-record, with no error naming the cancel.
type truncatingBody struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (b *truncatingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		b.cancel()
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func (b *truncatingBody) Close() error { return nil }

// Cancelling mid-stream leaves the decoder holding a truncated record; the
// run must report the cancellation, not the parse error it caused.
func TestProviderBuildWantReportsCancelOverTruncation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var head bytes.Buffer
	zw := gzip.NewWriter(&head)
	_, err := io.WriteString(zw, `{"id":"7673784e-db4b-43a1-8d55-1bb9fc1e284f","set":"tst","image_uris":{"grid":"https://cards.scryfall.io/x.webp"}}`+"\n"+`{"id":"half`)
	if err != nil {
		t.Fatal(err)
	}
	err = zw.Flush()
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Transport: rerouteFunc(func(req *http.Request) (*http.Response, error) {
		body := io.NopCloser(strings.NewReader(bulkListing))
		if req.URL.Path != "/bulk-data" {
			body = &truncatingBody{r: bytes.NewReader(head.Bytes()), cancel: cancel}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: body, Request: req}, nil
	})}
	p := &magic.Provider{HTTP: client, Log: log.New(io.Discard, "", 0)}

	_, err = p.BuildWant(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildWant = %v, want context.Canceled", err)
	}
}
