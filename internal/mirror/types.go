// Package mirror holds the mirror's manifest/state types, hashing, and object path helpers.
package mirror

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"path"
	"regexp"
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

// scryfallIDPattern is the key shape this mirror and the website's image
// handler both agree on. Storing anything else would put an image where
// nothing can read it back.
var scryfallIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// SingleObjectPath is where a single's image is stored, sharded by the first
// two characters of its id. Built from the id rather than transformed out of
// Scryfall's URL, so the layout is the mirror's own rather than a reflection
// of how Scryfall happens to file things, and pairs with SealedObjectPath.
//
// The variant sits above the face so a whole variant is one prefix, droppable
// or addable without disturbing the rest, and the face is kept so back faces
// can be mirrored later without moving anything.
func SingleObjectPath(scryfallID string) (string, error) {
	if !scryfallIDPattern.MatchString(scryfallID) {
		return "", fmt.Errorf("mirror: %q is not a scryfall id", scryfallID)
	}
	return fmt.Sprintf("singles/grid/front/%s/%s/%s.webp", scryfallID[0:1], scryfallID[1:2], scryfallID), nil
}

// safeSegmentPattern bounds what a non-Magic card id may be before it becomes
// a path segment. Magic has its own, stricter guard in SingleObjectPath; every
// other game's ids come from its own datastore and are not UUIDs, so this is
// the floor: one token, no separators, no leading dot, so an id can neither
// escape its prefix nor name a directory.
var safeSegmentPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,127}$`)

// SafeSegment reports whether s may be used as a path segment.
func SafeSegment(s string) bool { return safeSegmentPattern.MatchString(s) }

// shard returns the two directory levels an id is filed under. Magic's ids are
// 36 characters so their first two always exist; a game whose ids are short —
// Lorcana's are small integers — would otherwise have no second level, so a
// one character id is left padded rather than special cased into a different
// depth. Every game then has the same tree shape, which is what lets one path
// builder serve all of them.
func shard(id string) (string, string) {
	if len(id) < 2 {
		return "0", id
	}
	return id[0:1], id[1:2]
}

// GameSingleObjectPath is SingleObjectPath for a game whose card ids are not
// scryfall ids. The layout is deliberately identical — variant above face,
// face above the shard — so the website resolves every game's singles with one
// path builder and only the key validation differs.
//
// ext is the source image's extension, without a dot. Unlike Magic, which is
// always Scryfall's webp, a datastore-backed game's image URL is whatever its
// own CDN serves, so the extension travels with the image rather than being
// assumed.
func GameSingleObjectPath(id, variant, ext string) (string, error) {
	if !SafeSegment(id) {
		return "", fmt.Errorf("mirror: %q is not usable as a card id", id)
	}
	if !SafeSegment(variant) || !SafeSegment(ext) {
		return "", fmt.Errorf("mirror: variant %q or extension %q is not a safe path segment", variant, ext)
	}
	c1, c2 := shard(id)
	return fmt.Sprintf("singles/%s/front/%s/%s/%s.%s", variant, c1, c2, id, ext), nil
}

// GameSealedObjectPath is SealedObjectPath for a non-Magic game.
//
// It drops Magic's per-set directory and shards on the product id instead, for
// one reason: the key has to be enough. Magic's sealed key encodes the set code
// because its id is a TCGplayer product id, which means nothing to the rest of
// the system on its own. A datastore game's product id is already a uuid in the
// same namespace as its cards, so pairing it with a set code buys nothing and
// costs the ability to parse the pair back — Lorcana set codes can be a single
// character and its product ids contain dashes ("1" and "1-600001"), so
// "p-1-1-600001" has no unambiguous split. Sharding on the id alone keeps every
// key self describing: a reader derives the object path from the key and needs
// no set code at all.
func GameSealedObjectPath(id, ext string) (string, error) {
	if !SafeSegment(id) {
		return "", fmt.Errorf("mirror: %q is not usable as a sealed product id", id)
	}
	if !SafeSegment(ext) {
		return "", fmt.Errorf("mirror: extension %q is not a safe path segment", ext)
	}
	c1, c2 := shard(id)
	return fmt.Sprintf("sealed/%s/%s/%s.%s", c1, c2, id, ext), nil
}

// GameSealedKey returns the manifest/state image key for a non-Magic sealed
// product. It keeps Magic's p- prefix, so one predicate still separates sealed
// from singles across every game, but carries only the product's own id.
func GameSealedKey(id string) string { return "p-" + id }

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
