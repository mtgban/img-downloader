package mirror_test

import (
	"testing"

	"github.com/mtgban/img-downloader/internal/mirror"
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

// The stored extension does not follow the source's: whatever a game's CDN
// serves, the mirror converts it on the way in, so every path ends in webp.
func TestGameSingleObjectPath(t *testing.T) {
	for _, tt := range []struct{ id, want string }{
		// Riftbound ids carry dashes, Lorcana's are short integers
		{"ogn-066-298", "singles/full/front/o/g/ogn-066-298.webp"},
		{"460", "singles/full/front/4/6/460.webp"},
		// a one character id still has to fill both shard levels
		{"7", "singles/full/front/0/7/7.webp"},
	} {
		got, err := mirror.GameSingleObjectPath(tt.id, "full")
		if err != nil || got != tt.want {
			t.Errorf("GameSingleObjectPath(%q, full) = %q, %v; want %q", tt.id, got, err, tt.want)
		}
	}
}

// Same reasoning as the Magic guard: the id becomes a path segment, so an id
// that is not a plain token could place an object anywhere in the bucket.
func TestGameObjectPathsRejectUnsafeSegments(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../../mirror-state.json", "a/b", ".hidden", "a b"} {
		if got, err := mirror.GameSingleObjectPath(bad, "full"); err == nil {
			t.Errorf("GameSingleObjectPath(%q) = %q, want an error", bad, got)
		}
		if got, err := mirror.GameSealedObjectPath(bad); err == nil {
			t.Errorf("GameSealedObjectPath(%q) = %q, want an error", bad, got)
		}
	}
	// the variant is interpolated too
	if _, err := mirror.GameSingleObjectPath("460", "../x"); err == nil {
		t.Error("GameSingleObjectPath with a traversing variant = nil error, want an error")
	}
}

func TestGameSealedObjectPathAndKey(t *testing.T) {
	got, err := mirror.GameSealedObjectPath("ogn-600001")
	if err != nil || got != "sealed/o/g/ogn-600001.webp" {
		t.Errorf("GameSealedObjectPath = %q, %v", got, err)
	}
	key := mirror.GameSealedKey("ogn-600001")
	if key != "p-ogn-600001" {
		t.Errorf("GameSealedKey = %q", key)
	}
	if !mirror.IsSealedKey(key) {
		t.Error("GameSealedKey did not produce a key IsSealedKey recognises")
	}
}

// Magic's singles layout is a settled contract with the website and an
// existing 120k image bucket, and it is what keeps converting the corpus to
// webp cheap: Scryfall already serves webp, so those objects neither move nor
// get rewritten. Sealed did move, because TCGplayer serves jpg and that is now
// converted on the way in.
func TestMagicObjectPathsUnchanged(t *testing.T) {
	const id = "7673784e-db4b-43a1-8d55-1bb9fc1e284f"
	got, err := mirror.SingleObjectPath(id)
	if err != nil || got != "singles/grid/front/7/6/"+id+".webp" {
		t.Errorf("SingleObjectPath = %q, %v", got, err)
	}
	if p := mirror.SealedObjectPath("NEO", "111"); p != "sealed/NEO/111.webp" {
		t.Errorf("SealedObjectPath = %q", p)
	}
	if k := mirror.SealedKey("NEO", "111"); k != "p-NEO-111" {
		t.Errorf("SealedKey = %q", k)
	}
}
