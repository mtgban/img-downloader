// Package mirror holds the mirror's manifest/state types, hashing, and object path helpers.
package mirror

import (
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

// StateEntry records one mirrored image in mirror-state.json. Missing marks a
// source that answered but has no image at that URL, recorded so later runs
// skip it instead of refetching it forever; such an entry has no Digest and is
// not bundled.
type StateEntry struct {
	Digest    string `json:"digest"`
	FetchedAt string `json:"fetchedAt"`
	Source    string `json:"source"`
	Missing   bool   `json:"missing,omitempty"`
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

// JoinPath appends elems to base, preserving scheme/host (one-letter schemes are Windows drives).
func JoinPath(base string, elems ...string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || len(u.Scheme) == 1 {
		return path.Join(append([]string{base}, elems...)...)
	}
	u.Path = path.Join(append([]string{u.Path}, elems...)...)
	return u.String()
}

// SingleObjectPath is the Scryfall image URL path with the leading slash stripped.
// Rejects paths that are empty, absolute, or escape the bucket root.
func SingleObjectPath(sourceURL string) (string, error) {
	u, err := url.Parse(sourceURL)
	if err != nil {
		return "", err
	}
	p := strings.TrimPrefix(u.Path, "/")
	if p == "" || path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") || path.Clean(p) != p {
		return "", fmt.Errorf("mirror: unsafe object path %q from source URL", p)
	}
	return p, nil
}

// SealedObjectPath returns the bucket object path for a sealed product image.
// Sealed lives under one shared prefix rather than a directory per set code,
// so the bucket root holds only the handful of top level trees.
func SealedObjectPath(setCode, tcgID string) string {
	return fmt.Sprintf("sealed/%s/%s.jpg", setCode, tcgID)
}

// SealedKey returns the manifest/state image key for a sealed product.
func SealedKey(setCode, tcgID string) string {
	return fmt.Sprintf("p-%s-%s", setCode, tcgID)
}

// IsSealedKey reports whether key is a sealed product image key (p-<SETCODE>-<tcgId>).
func IsSealedKey(key string) bool {
	return strings.HasPrefix(key, "p-")
}
