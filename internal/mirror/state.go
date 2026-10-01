package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"

	"github.com/Backblaze/blazer/b2"
	"github.com/mtgban/simplecloud"
)

// isNotExist reports whether err means the object does not exist yet.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || b2.IsNotExist(err)
}

// loadBucketJSON decodes one JSON document into out, reporting whether it
// exists. Only a missing document is a first run; any other failure is fatal,
// so mirror state cannot silently reset.
func loadBucketJSON(ctx context.Context, bucket simplecloud.Reader, base, name string, out any) (bool, error) {
	reader, err := simplecloud.InitReader(ctx, bucket, JoinPath(base, name))
	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer reader.Close()
	// B2 opens lazily, so a missing object surfaces here on first read.
	if err := json.NewDecoder(reader).Decode(out); err != nil {
		if isNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// loadOrStartEmpty is loadBucketJSON for a document a first run starts
// without, saying so in the log.
func loadOrStartEmpty(ctx context.Context, bucket simplecloud.Reader, base, name string, out any) error {
	found, err := loadBucketJSON(ctx, bucket, base, name, out)
	if err == nil && !found {
		log.Printf("%s missing, starting empty", name)
	}
	return err
}

// saveBucketJSON encodes value before opening the object, so a value that
// fails to encode leaves the stored document untouched.
func saveBucketJSON(ctx context.Context, bucket simplecloud.Writer, base, name string, value any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(value); err != nil {
		return err
	}
	return writeObject(ctx, bucket, JoinPath(base, name), buf.Bytes())
}

// writeObject stores data at path. A failed write is aborted rather than
// closed, because Close is what publishes the object, truncated or not.
func writeObject(ctx context.Context, bucket simplecloud.Writer, path string, data []byte) error {
	writer, err := simplecloud.InitWriter(ctx, bucket, path)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return errors.Join(err, discard(writer))
	}
	return writer.Close()
}

// discard abandons a write, closing a writer that cannot abort, which is all
// simplecloud itself does with one.
func discard(w io.WriteCloser) error {
	if a, ok := w.(simplecloud.Aborter); ok {
		return a.Abort()
	}
	return w.Close()
}

// LoadState reads mirror-state.json from base, returning an empty map if missing.
func LoadState(ctx context.Context, bucket simplecloud.Reader, base string) (State, error) {
	state := State{}
	if err := loadOrStartEmpty(ctx, bucket, base, "mirror-state.json", &state); err != nil {
		return nil, err
	}
	return state, nil
}

// SaveState writes mirror-state.json to base.
func SaveState(ctx context.Context, bucket simplecloud.Writer, base string, state State) error {
	return saveBucketJSON(ctx, bucket, base, "mirror-state.json", state)
}

// LoadManifest reads images-manifest.json from base, returning an empty map if missing.
func LoadManifest(ctx context.Context, bucket simplecloud.Reader, base string) (Manifest, error) {
	manifest := Manifest{}
	if err := loadOrStartEmpty(ctx, bucket, base, "images-manifest.json", &manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// SaveManifest writes images-manifest.json to base.
func SaveManifest(ctx context.Context, bucket simplecloud.Writer, base string, manifest Manifest) error {
	return saveBucketJSON(ctx, bucket, base, "images-manifest.json", manifest)
}
