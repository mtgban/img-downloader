package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/mtgban/simplecloud"
	"github.com/the-muppet2/img-downloader/internal/scryfall"
)

const (
	fetchRetries = 6
	// snapshot every N fetches so an interrupted crawl can resume
	stateSaveEvery  = 200
	requestInterval = 100 * time.Millisecond
)

type fetcher struct {
	bucket  simplecloud.ReadWriter
	base    string
	client  *http.Client
	limit   *Limiter
	backoff func(int) time.Duration
	log     *log.Logger

	mu     sync.Mutex
	saveMu sync.Mutex
	state  State
	done   int
	failed int
}

func newFetcher(bucket simplecloud.ReadWriter, base string, state State, logger *log.Logger) *fetcher {
	return &fetcher{
		bucket:  bucket,
		base:    base,
		client:  &http.Client{Timeout: 60 * time.Second},
		limit:   &Limiter{Interval: requestInterval},
		backoff: Backoff,
		log:     logger,
		state:   state,
	}
}

// FetchAll downloads keys from want, one goroutine per source host.
func FetchAll(ctx context.Context, bucket simplecloud.ReadWriter, base string, state State, want map[string]Image, keys []string, logger *log.Logger) (fetched, failed int, err error) {
	f := newFetcher(bucket, base, state, logger)
	return f.run(ctx, want, keys)
}

func (f *fetcher) run(ctx context.Context, want map[string]Image, keys []string) (int, int, error) {
	queues := map[string][]string{}
	for _, key := range keys {
		img := want[key]
		u, err := url.Parse(img.URL)
		if err != nil {
			f.log.Printf("%s: bad image URL %q", key, img.URL)
			f.mu.Lock()
			f.failed++
			f.mu.Unlock()
			continue
		}
		queues[u.Host] = append(queues[u.Host], key)
	}

	var wg sync.WaitGroup
	for host, queue := range queues {
		wg.Add(1)
		go func(host string, queue []string) {
			defer wg.Done()
			for _, key := range queue {
				if err := f.fetchOne(ctx, host, want[key]); err != nil {
					f.log.Printf("%s: %v", key, err)
					f.mu.Lock()
					f.failed++
					f.mu.Unlock()
				}
			}
		}(host, queue)
	}
	wg.Wait()

	f.mu.Lock()
	done, failed := f.done, f.failed
	f.mu.Unlock()
	// nothing fetched means nothing new to persist
	if done > 0 {
		if err := f.saveSnapshot(ctx); err != nil {
			return done, failed, err
		}
	}
	f.log.Printf("fetched %d images, %d failures", done, failed)
	if failed > 0 {
		return done, failed, fmt.Errorf("%d fetches failed", failed)
	}
	return done, failed, nil
}

func (f *fetcher) fetchOne(ctx context.Context, host string, img Image) error {
	data, err := f.download(ctx, host, img.URL)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	objPath := JoinPath(f.base, img.ObjectPath)
	writer, err := simplecloud.InitWriter(ctx, f.bucket, objPath)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	entry := StateEntry{
		Digest:    digest,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Source:    img.URL,
	}

	f.mu.Lock()
	f.state[img.Key] = entry
	f.done++
	save := f.done%stateSaveEvery == 0
	f.mu.Unlock()

	// periodic saves keep an interrupted crawl resumable
	if save {
		if err := f.saveSnapshot(ctx); err != nil {
			f.log.Println("state save failed:", err)
		}
	}
	return nil
}

// download GETs the image with per host spacing and backoff on 429/5xx.
func (f *fetcher) download(ctx context.Context, host, srcURL string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		time.Sleep(f.limit.Reserve(host, time.Now()))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", scryfall.UserAgent)
		req.Header.Set("Accept", "*/*")

		resp, err := f.client.Do(req)
		if err != nil {
			if attempt >= fetchRetries {
				return nil, err
			}
			time.Sleep(f.backoff(attempt))
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			return data, err
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			resp.Body.Close()
			if attempt >= fetchRetries {
				return nil, fmt.Errorf("giving up after %d attempts: HTTP %d", attempt+1, resp.StatusCode)
			}
			delay := f.backoff(attempt)
			if s := resp.Header.Get("Retry-After"); s != "" {
				if secs, err := strconv.Atoi(s); err == nil {
					ra := time.Duration(secs) * time.Second
					if ra > 5*time.Minute {
						ra = 5 * time.Minute
					}
					if ra > delay {
						delay = ra
					}
				}
			}
			time.Sleep(delay)
		default:
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
	}
}

func (f *fetcher) saveSnapshot(ctx context.Context) error {
	f.mu.Lock()
	snapshot := make(State, len(f.state))
	for k, v := range f.state {
		snapshot[k] = v
	}
	f.mu.Unlock()
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	return SaveState(ctx, f.bucket, f.base, snapshot)
}
