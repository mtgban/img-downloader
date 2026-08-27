package mirror

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mtgban/simplecloud"
)

// A run killed partway through would otherwise lose every bundle it just
// built, and since the phase outlasts a CI job's timeout it would repeat that
// work forever without ever converging. Counted rather than timed: what this
// bounds is how many rebuilt sets a kill can cost.
const bundleSaveEvery = 20

// A first run rebuilds every set, reading each one's images back out of the
// bucket, which is hours of otherwise silent work. Every snapshot reports
// itself, and this fills the gaps between them: sets differ in size by orders
// of magnitude, so twenty promo sets pass in seconds while twenty large ones
// take minutes, and a purely counted cadence goes quiet exactly where the run
// is slowest and reassurance is worth most.
const bundleProgressInterval = 30 * time.Second

// Worker count is bounded by memory, not CPU: a large set holds its raw images plus its zip in memory at once.
const bundleWorkers = 8

// RebuildBundles rebuilds each set's zip, snapshotting the manifest as it goes
// so a killed run resumes rather than restarting. Failures are per set. Sets
// are rebuilt on a bounded worker pool; the snapshot cadence and the
// cancellation/failure handling apply to completions, so their order does not
// depend on which worker finishes which set first.
func RebuildBundles(ctx context.Context, bucket simplecloud.ReadWriter, base string, state State, want map[string]Image, manifest Manifest, codes []string, logger *log.Logger) (int, error) {
	setDigests := SetDigests(state, want)
	if logger == nil {
		logger = log.Default()
	}

	var mu sync.Mutex
	var saveMu sync.Mutex
	rebuilt := 0
	completed := 0
	var failed []string

	work := make(chan string)
	go func() {
		defer close(work)
		for _, code := range codes {
			select {
			case work <- code:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	lastProgress := time.Now()
	for range bundleWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for code := range work {
				// stop taking new work once the run is cancelled
				if ctx.Err() != nil {
					return
				}
				info, err := rebuildOne(ctx, bucket, base, want, code, setDigests[code])
				if err != nil {
					// the closing summary can only name the set, and a set code
					// on its own cannot tell a missing object from a transient
					// read. This is the only place the cause still exists.
					logger.Printf("bundle %s: %v", code, err)
				}

				mu.Lock()
				if err != nil {
					failed = append(failed, code)
				} else {
					manifest[code] = info
					rebuilt++
				}
				completed++
				n := completed
				// copy under the lock so the save below cannot race concurrent manifest writes
				var snapshot Manifest
				if n%bundleSaveEvery == 0 {
					snapshot = make(Manifest, len(manifest))
					for k, v := range manifest {
						snapshot[k] = v
					}
				}
				// A snapshot always announces itself, so the durable
				// checkpoints stay visible in the log rather than being
				// swallowed by the timer.
				now := time.Now()
				report := snapshot != nil || now.Sub(lastProgress) >= bundleProgressInterval
				if report {
					lastProgress = now
				}
				mu.Unlock()

				if report {
					logger.Printf("rebuilt %d/%d bundles", n, len(codes))
				}
				if snapshot != nil {
					saveMu.Lock()
					if err := saveManifestSnapshot(ctx, bucket, base, snapshot); err != nil {
						logger.Println("manifest save failed:", err)
					}
					saveMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		logger.Printf("interrupted after %d of %d bundles", completed, len(codes))
		return rebuilt, ctx.Err()
	}
	if len(failed) > 0 {
		return rebuilt, fmt.Errorf("bundle rebuild failed for %d of %d sets: %s", len(failed), len(codes), strings.Join(failed, ", "))
	}
	return rebuilt, nil
}

// saveManifestSnapshot persists progress on a context that outlives the run's,
// so a snapshot triggered just as the run is cancelled still lands.
func saveManifestSnapshot(ctx context.Context, bucket simplecloud.Writer, base string, manifest Manifest) error {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	return SaveManifest(saveCtx, bucket, base, manifest)
}

// rebuildOne builds and uploads one set's zip and returns its manifest entry.
func rebuildOne(ctx context.Context, bucket simplecloud.ReadWriter, base string, want map[string]Image, code string, digests map[string]string) (ImageInfo, error) {
	entries := make([]BundleEntry, 0, len(digests))
	for key := range digests {
		img, ok := want[key]
		if !ok {
			return ImageInfo{}, fmt.Errorf("key %s missing from want map", key)
		}
		reader, err := simplecloud.InitReader(ctx, bucket, JoinPath(base, img.ObjectPath))
		if err != nil {
			return ImageInfo{}, err
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return ImageInfo{}, err
		}
		// Every stored object is webp, so every entry is named for one, and
		// the client has one content type to cache under rather than a guess
		// to make from the name.
		entries = append(entries, BundleEntry{Name: key + "." + ImageExt, Data: data})
	}

	zipData, err := BuildBundle(entries)
	if err != nil {
		return ImageInfo{}, err
	}
	hash := BundleHash(digests)
	writer, err := simplecloud.InitWriter(ctx, bucket, JoinPath(base, "bundles", code+"-"+hash+".zip"))
	if err != nil {
		return ImageInfo{}, err
	}
	if _, err := writer.Write(zipData); err != nil {
		writer.Close()
		return ImageInfo{}, err
	}
	if err := writer.Close(); err != nil {
		return ImageInfo{}, err
	}
	return ImageInfo{Hash: hash, Count: len(entries), Bytes: int64(len(zipData))}, nil
}
