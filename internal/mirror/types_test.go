package mirror_test

import (
	"testing"

	"github.com/mtgban/img-downloader/internal/mirror"
)

func TestSetHashDeterministicAndOrderFree(t *testing.T) {
	a := mirror.SetHash(map[string]string{"uuid-a": "d1", "uuid-b": "d2"})
	b := mirror.SetHash(map[string]string{"uuid-b": "d2", "uuid-a": "d1"})
	if a == "" || a != b {
		t.Errorf("hash not deterministic: %q vs %q", a, b)
	}
}

func TestSetHashSensitive(t *testing.T) {
	base := map[string]string{"uuid-a": "d1", "uuid-b": "d2"}
	ref := mirror.SetHash(base)
	for name, m := range map[string]map[string]string{
		"digest changed": {"uuid-a": "d1x", "uuid-b": "d2"},
		"uuid changed":   {"uuid-ax": "d1", "uuid-b": "d2"},
		"member removed": {"uuid-a": "d1"},
		"member added":   {"uuid-a": "d1", "uuid-b": "d2", "uuid-c": "d3"},
	} {
		if mirror.SetHash(m) == ref {
			t.Errorf("%s: hash did not change", name)
		}
	}
}

func TestSetHashFieldBoundary(t *testing.T) {
	if mirror.SetHash(map[string]string{"ab": "c"}) == mirror.SetHash(map[string]string{"a": "bc"}) {
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
	const id = "7673784e-db4b-43a1-8d55-1bb9fc1e284f"
	got, err := mirror.SingleObjectPath(id)
	if err != nil || got != "singles/grid/front/7/6/"+id+".webp" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestSingleObjectPathRejectsAnythingButAnID(t *testing.T) {
	// The path is built from this value, so a key that is not an id could
	// otherwise place an object anywhere in the bucket, or somewhere the
	// website's own id pattern would refuse to read back.
	for _, bad := range []string{
		"",
		"7673784e-1234",
		"../../mirror-state.json",
		"7673784e-db4b-43a1-8d55-1bb9fc1e284f/../../x",
		"7673784E-DB4B-43A1-8D55-1BB9FC1E284F",
		"https://cards.scryfall.io/normal/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.jpg",
	} {
		if got, err := mirror.SingleObjectPath(bad); err == nil {
			t.Errorf("SingleObjectPath(%q) = %q, want an error", bad, got)
		}
	}
}
