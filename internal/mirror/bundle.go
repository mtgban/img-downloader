package mirror

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mtgban/simplecloud"
)

// RebuildBundles rebuilds each set's zip; failures are per set, caller saves manifest.
func RebuildBundles(ctx context.Context, bucket simplecloud.ReadWriter, base string, state State, want map[string]Image, manifest Manifest, codes []string) (int, error) {
	setDigests := SetDigests(state, want)

	rebuilt := 0
	var failed []string
	for _, code := range codes {
		info, err := rebuildOne(ctx, bucket, base, want, code, setDigests[code])
		if err != nil {
			failed = append(failed, code)
			continue
		}
		manifest[code] = info
		rebuilt++
	}
	if len(failed) > 0 {
		return rebuilt, fmt.Errorf("bundle rebuild failed for %d of %d sets: %s", len(failed), len(codes), strings.Join(failed, ", "))
	}
	return rebuilt, nil
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
