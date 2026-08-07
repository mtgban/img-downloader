package mirror

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/mtgban/simplecloud"
)

// A first run rebuilds every set, reading each one's images back out of the
// bucket, which is hours of otherwise silent work. Snapshot on the same
// cadence as the progress line: a run killed partway through would otherwise
// lose every bundle it just built, and since the phase outlasts a CI job's
// timeout it would repeat that work forever without ever converging.
const bundleSaveEvery = 20

// RebuildBundles rebuilds each set's zip, snapshotting the manifest as it goes
// so a killed run resumes rather than restarting. Failures are per set.
func RebuildBundles(ctx context.Context, bucket simplecloud.ReadWriter, base string, state State, want map[string]Image, manifest Manifest, codes []string, logger *log.Logger) (int, error) {
	setDigests := SetDigests(state, want)
	if logger == nil {
		logger = log.Default()
	}

	rebuilt := 0
	var failed []string
	for i, code := range codes {
		// bail immediately rather than walking the remainder failing every
		// read, which would outlast the grace period before the final save
		if ctx.Err() != nil {
			logger.Printf("interrupted after %d of %d bundles", i, len(codes))
			return rebuilt, ctx.Err()
		}
		info, err := rebuildOne(ctx, bucket, base, want, code, setDigests[code])
		if err != nil {
			failed = append(failed, code)
			continue
		}
		manifest[code] = info
		rebuilt++
		if (i+1)%bundleSaveEvery == 0 {
			logger.Printf("rebuilt %d/%d bundles", i+1, len(codes))
			if err := saveManifestSnapshot(ctx, bucket, base, manifest); err != nil {
				logger.Println("manifest save failed:", err)
			}
		}
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
		entries = append(entries, BundleEntry{Name: key + ".jpg", Data: data})
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
