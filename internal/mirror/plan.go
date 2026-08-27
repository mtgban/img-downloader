package mirror

import (
	"net/url"
	"path"
	"sort"
	"strings"
)

// Image is one wanted mirror entry. A provider supplies every field: the
// mirror stores Key in state and the manifest, fetches URL, and writes the
// bytes to ObjectPath, without knowing which game any of them came from.
type Image struct {
	Key        string
	URL        string
	ObjectPath string
	SetCode    string
}

// NeedFetch returns the keys missing from state, whose source URL changed, or
// whose stored object belongs somewhere else now, sorted.
func NeedFetch(state State, want map[string]Image) []string {
	var out []string
	for key, img := range want {
		prev, found := state[key]
		if !found || prev.Source != img.URL || misfiled(prev, img) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// misfiled reports whether the stored object sits somewhere other than where
// this run wants it, which comparing source urls cannot see: converting the
// corpus to webp moved every object the mirror had stored in its source's own
// format without changing a single url to fetch it from.
//
// A missing marker records that a source had no image rather than an object on
// disk, so it is nothing to move; RetryMissing is what asks those again.
//
// Entries written before ObjectPath was recorded have only their source to go
// on, and for those the source's extension is a faithful account of what was
// stored, because the mirror wrote fetched bytes through untouched.
func misfiled(prev StateEntry, img Image) bool {
	if prev.Missing {
		return false
	}
	if prev.ObjectPath != "" {
		return prev.ObjectPath != img.ObjectPath
	}
	return urlExt(prev.Source) != path.Ext(img.ObjectPath)
}

// urlExt is the extension of a url's path, lowercased and with its dot, so a
// query string (Scryfall stamps an epoch on every image url) is not mistaken
// for part of it.
func urlExt(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(path.Ext(u.Path))
}

// SetDigests groups the already fetched wanted keys by set code.
func SetDigests(state State, want map[string]Image) map[string]map[string]string {
	out := map[string]map[string]string{}
	for key, img := range want {
		entry, found := state[key]
		if !found || entry.Missing {
			continue
		}
		m := out[img.SetCode]
		if m == nil {
			m = map[string]string{}
			out[img.SetCode] = m
		}
		m[key] = entry.Digest
	}
	return out
}

// BundlesToRebuild returns the set codes whose digests no longer match the manifest, sorted.
func BundlesToRebuild(m Manifest, setDigests map[string]map[string]string) []string {
	var out []string
	for code, digests := range setDigests {
		if m[code].Hash != BundleHash(digests) {
			out = append(out, code)
		}
	}
	sort.Strings(out)
	return out
}

// Domains counts the wanted image URLs per source host.
func Domains(want map[string]Image) map[string]int {
	out := map[string]int{}
	for _, img := range want {
		u, err := url.Parse(img.URL)
		if err != nil {
			continue
		}
		out[u.Host]++
	}
	return out
}

// NotPublishedCount counts the wanted keys recorded as having no image at source.
func NotPublishedCount(state State, want map[string]Image) int {
	n := 0
	for key := range want {
		if state[key].Missing {
			n++
		}
	}
	return n
}

// AllSetCodes returns every set code present in a digest map, sorted.
func AllSetCodes(setDigests map[string]map[string]string) []string {
	out := make([]string, 0, len(setDigests))
	for code := range setDigests {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}
