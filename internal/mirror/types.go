// Package mirror holds the pure logic of the bulk image mirror: manifest
// and state types, bundle hashing, and bucket object path helpers.
package mirror

import (
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
)

// ImageInfo is one set's entry in images-manifest.json.
type ImageInfo struct {
	Hash  string `json:"h"`
	Count int    `json:"n"`
	Bytes int64  `json:"b"`
}

// Manifest is the images-manifest.json document, keyed by set code.
type Manifest map[string]ImageInfo

// StateEntry records one mirrored image in mirror-state.json.
type StateEntry struct {
	Digest    string `json:"digest"`
	FetchedAt string `json:"fetchedAt"`
	Source    string `json:"source"`
}

// State is the mirror-state.json document, keyed by image key.
type State map[string]StateEntry

// BundleHash hashes sorted "key digest" lines with fnv64a, hex encoded.
func BundleHash(digests map[string]string) string {
	keys := make([]string, 0, len(digests))
	for k := range digests {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := fnv.New64a()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{' '})
		h.Write([]byte(digests[k]))
		h.Write([]byte{'\n'})
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

// JoinPath appends elements to a bucket base path, preserving the scheme
// and host of remote bases. One letter schemes are Windows drive paths.
func JoinPath(base string, elems ...string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || len(u.Scheme) == 1 {
		return path.Join(append([]string{base}, elems...)...)
	}
	u.Path = path.Join(append([]string{u.Path}, elems...)...)
	return u.String()
}

// SingleObjectPath returns the bucket object path for a Scryfall image URL:
// the URL path with the leading slash stripped.
func SingleObjectPath(sourceURL string) (string, error) {
	u, err := url.Parse(sourceURL)
	if err != nil {
		return "", err
	}
	p := strings.TrimPrefix(u.Path, "/")
	if p == "" {
		return "", errors.New("mirror: empty path in source URL")
	}
	return p, nil
}

// SealedObjectPath returns the bucket object path for a sealed product image.
func SealedObjectPath(setCode, tcgID string) string {
	return fmt.Sprintf("%s/sealed/%s.jpg", setCode, tcgID)
}

// SealedKey returns the manifest/state image key for a sealed product.
func SealedKey(setCode, tcgID string) string {
	return fmt.Sprintf("p-%s-%s", setCode, tcgID)
}
