package mirror_test

import (
	"testing"

	"github.com/the-muppet2/img-downloader/internal/mirror"
)

func TestBundleHashDeterministicAndOrderFree(t *testing.T) {
	a := mirror.BundleHash(map[string]string{"uuid-a": "d1", "uuid-b": "d2"})
	b := mirror.BundleHash(map[string]string{"uuid-b": "d2", "uuid-a": "d1"})
	if a == "" || a != b {
		t.Errorf("hash not deterministic: %q vs %q", a, b)
	}
}

func TestBundleHashSensitive(t *testing.T) {
	base := map[string]string{"uuid-a": "d1", "uuid-b": "d2"}
	ref := mirror.BundleHash(base)
	for name, m := range map[string]map[string]string{
		"digest changed": {"uuid-a": "d1x", "uuid-b": "d2"},
		"uuid changed":   {"uuid-ax": "d1", "uuid-b": "d2"},
		"member removed": {"uuid-a": "d1"},
		"member added":   {"uuid-a": "d1", "uuid-b": "d2", "uuid-c": "d3"},
	} {
		if mirror.BundleHash(m) == ref {
			t.Errorf("%s: hash did not change", name)
		}
	}
}

func TestBundleHashFieldBoundary(t *testing.T) {
	if mirror.BundleHash(map[string]string{"ab": "c"}) == mirror.BundleHash(map[string]string{"a": "bc"}) {
		t.Error("uuid/digest boundary collision")
	}
}

func TestJoinPath(t *testing.T) {
	tests := []struct{ base, want string }{
		{"offline-mirror", "offline-mirror/images/x.webp"},
		{"b2://bucket/offline", "b2://bucket/offline/images/x.webp"},
		{"C:/Users/elmo/scratch", "C:/Users/elmo/scratch/images/x.webp"},
	}
	for _, tt := range tests {
		if got := mirror.JoinPath(tt.base, "images", "x.webp"); got != tt.want {
			t.Errorf("JoinPath(%q) = %q, want %q", tt.base, got, tt.want)
		}
	}
}

func TestSingleObjectPath(t *testing.T) {
	got, err := mirror.SingleObjectPath("https://cards.scryfall.io/normal/front/7/6/7673784e-1234.jpg?1783903008")
	if err != nil || got != "normal/front/7/6/7673784e-1234.jpg" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestSingleObjectPathRejectsTraversal(t *testing.T) {
	urls := []string{
		"https://host/../magic/images-manifest.json",
		"https://host/normal/../../magic/mirror-state.json",
		"https://host//etc/passwd",
		"https://host/..",
	}
	for _, u := range urls {
		if got, err := mirror.SingleObjectPath(u); err == nil {
			t.Errorf("SingleObjectPath(%q) = %q, nil, want error", u, got)
		}
	}
}

func TestSealedKeyAndPath(t *testing.T) {
	if k := mirror.SealedKey("MH3", "541185"); k != "p-MH3-541185" {
		t.Fatalf("key %q", k)
	}
	if p := mirror.SealedObjectPath("MH3", "541185"); p != "MH3/sealed/541185.jpg" {
		t.Fatalf("path %q", p)
	}
}
