package mirror

import (
	"fmt"
	"net/url"
	"sort"

	"github.com/the-muppet2/img-downloader/internal/mtgjson"
)

// Image is one wanted mirror entry.
type Image struct {
	Key        string
	URL        string
	ObjectPath string
	SetCode    string
}

// BuildWant assembles the wanted image map from mtgjson sets and scryfall URLs.
func BuildWant(sets []mtgjson.SetImages, scryURL map[string]string, setsFilter map[string]bool) (map[string]Image, []string) {
	want := map[string]Image{}
	var missing []string
	for _, s := range sets {
		if setsFilter != nil && !setsFilter[s.Code] {
			continue
		}
		for _, id := range s.ScryfallIDs {
			srcURL, ok := scryURL[id]
			if !ok {
				missing = append(missing, id)
				continue
			}
			// only well formed scryfall URLs become object paths
			objectPath, err := SingleObjectPath(srcURL)
			if err != nil {
				continue
			}
			want[id] = Image{Key: id, URL: srcURL, ObjectPath: objectPath, SetCode: s.Code}
		}
		for _, sealed := range s.Sealed {
			key := SealedKey(s.Code, sealed.TcgplayerProductID)
			want[key] = Image{
				Key:        key,
				URL:        fmt.Sprintf("https://product-images.tcgplayer.com/%s.jpg", sealed.TcgplayerProductID),
				ObjectPath: SealedObjectPath(s.Code, sealed.TcgplayerProductID),
				SetCode:    s.Code,
			}
		}
	}
	sort.Strings(missing)
	return want, missing
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
		if !found {
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
