package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/mtgban/img-downloader/internal/scryfall"
	"github.com/mtgban/simplecloud"
)

const (
	fetchRetries = 6
	// snapshot every N fetches so an interrupted crawl can resume
	stateSaveEvery  = 200
	requestInterval = 100 * time.Millisecond
	// budget for persisting state/manifest after the run context is already done
	saveTimeout = 30 * time.Second
	// A source host that is down, blocking us, or has moved its URL shape fails
	// everything, while a healthy one still 404s the odd image it never
	// published. Counting consecutive failures separates the two: any success
	// resets it, so scattered misses cannot accumulate however long the run
	// goes, and a host that breaks midway still trips despite the hours of
	// successes behind it, which a cumulative rate would drown out. The longest
	// natural run seen against TCGplayer's sealed images is ~13, from a couple
	// of old sets sorting next to each other.
	maxConsecutiveFailures = 50
)

// ErrTooManyFailures aborts a run against a source that is failing every
// request, as opposed to one merely missing the occasional image.
var ErrTooManyFailures = errors.New("mirror: source failing persistently")

// hostStat tracks one source host's failure streak for the circuit breaker.
type hostStat struct{ consecutive int }

type fetcher struct {
	bucket         simplecloud.ReadWriter
	base           string
	client         *http.Client
	limit          *Limiter
	backoff        func(int) time.Duration
	log            *log.Logger
	maxConsecutive int

	mu      sync.Mutex
	saveMu  sync.Mutex
	state   State
	done    int
	failed  int
	hosts   map[string]*hostStat
	tripped error
	abort   context.CancelFunc
}

func newFetcher(bucket simplecloud.ReadWriter, base string, state State, logger *log.Logger) *fetcher {
	return &fetcher{
		bucket:         bucket,
		base:           base,
		client:         &http.Client{Timeout: 60 * time.Second},
		limit:          &Limiter{Interval: requestInterval},
		backoff:        Backoff,
		log:            logger,
		maxConsecutive: maxConsecutiveFailures,
		state:          state,
	}
}

// record accounts one attempt against host and trips the run when that host has
// failed maxConsecutive times without a success in between. Reports whether the
// run has been aborted.
func (f *fetcher) record(host string, err error) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.hosts == nil {
		f.hosts = map[string]*hostStat{}
	}
	stat := f.hosts[host]
	if stat == nil {
		stat = &hostStat{}
		f.hosts[host] = stat
	}

	if err == nil {
		stat.consecutive = 0
		return f.tripped != nil
	}
	stat.consecutive++
	f.failed++

	if f.tripped != nil {
		return true
	}
	if stat.consecutive < f.maxConsecutive {
		return false
	}
	f.tripped = fmt.Errorf("%w: %s failed %d requests in a row",
		ErrTooManyFailures, host, stat.consecutive)
	f.log.Printf("aborting: %v", f.tripped)
	f.abort()
	return true
}

// FetchAll downloads keys from want, one goroutine per source host.
func FetchAll(ctx context.Context, bucket simplecloud.ReadWriter, base string, state State, want map[string]Image, keys []string, logger *log.Logger) (fetched, failed int, err error) {
	f := newFetcher(bucket, base, state, logger)
	return f.run(ctx, want, keys)
}

func (f *fetcher) run(ctx context.Context, want map[string]Image, keys []string) (int, int, error) {
	// a derived context so one host tripping the breaker stops every other queue
	runCtx, abort := context.WithCancel(ctx)
	defer abort()
	f.abort = abort

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
				// stop taking new work once the run is cancelled or aborted
				if runCtx.Err() != nil {
					return
				}
				err := f.fetchOne(runCtx, host, want[key])
				// a fetch losing a race with cancellation is not a real outcome
				if err != nil && runCtx.Err() != nil {
					return
				}
				if err != nil {
					f.log.Printf("%s: %v", key, err)
				}
				if f.record(host, err) {
					return
				}
			}
		}(host, queue)
	}
	wg.Wait()

	f.mu.Lock()
	done, failed, tripped := f.done, f.failed, f.tripped
	f.mu.Unlock()
	// nothing fetched means nothing new to persist
	if done > 0 {
		if err := f.saveSnapshot(ctx); err != nil {
			return done, failed, err
		}
	}
	f.log.Printf("fetched %d images, %d failures", done, failed)
	// an interrupt outranks a trip: the operator's stop is the real reason the
	// run ended, and only the parent context can tell the two apart
	if ctx.Err() != nil {
		return done, failed, ctx.Err()
	}
	if tripped != nil {
		return done, failed, tripped
	}
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
		if err := sleepCtx(ctx, f.limit.Reserve(host, time.Now())); err != nil {
			return nil, err
		}

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
			if err := sleepCtx(ctx, f.backoff(attempt)); err != nil {
				return nil, err
			}
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
			if err := sleepCtx(ctx, delay); err != nil {
				return nil, err
			}
		default:
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
	}
}

// sleepCtx waits out d, or returns ctx.Err() early if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (f *fetcher) saveSnapshot(ctx context.Context) error {
	f.mu.Lock()
	snapshot := make(State, len(f.state))
	for k, v := range f.state {
		snapshot[k] = v
	}
	f.mu.Unlock()

	// save survives a cancelled run so the crawl stays resumable
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()

	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	return SaveState(saveCtx, f.bucket, f.base, snapshot)
}
