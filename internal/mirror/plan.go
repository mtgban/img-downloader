package mirror

import (
	"net/url"
	"sort"
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

// NeedFetch returns the keys missing from state or whose source URL changed, sorted.
func NeedFetch(state State, want map[string]Image) []string {
	var out []string
	for key, img := range want {
		prev, found := state[key]
		if !found || prev.Source != img.URL {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
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
