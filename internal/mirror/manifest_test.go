package mirror

import "testing"

func TestBuildManifestDescribesWhatWasStored(t *testing.T) {
	state := State{
		"a": {Digest: "d1", Size: 100},
		"b": {Digest: "d2", Size: 250},
		// never published, so it has no object and must not be counted
		"c": {Missing: true},
	}
	want := map[string]Image{
		"a": {Key: "a", SetCode: "NEO"},
		"b": {Key: "b", SetCode: "NEO"},
		"c": {Key: "c", SetCode: "NEO"},
	}

	m := BuildManifest(state, want)
	if got := m["NEO"].Count; got != 2 {
		t.Errorf("Count = %d, want 2 stored images", got)
	}
	if got := m["NEO"].Bytes; got != 350 {
		t.Errorf("Bytes = %d, want 350", got)
	}
	if m["NEO"].Hash == "" {
		t.Error("no hash for clients to diff against")
	}
}

func TestBuildManifestHashTracksContent(t *testing.T) {
	want := map[string]Image{"a": {Key: "a", SetCode: "NEO"}, "b": {Key: "b", SetCode: "NEO"}}
	base := BuildManifest(State{"a": {Digest: "d1"}, "b": {Digest: "d2"}}, want)["NEO"].Hash

	// an unchanged set must not make every client re-download it
	same := BuildManifest(State{"b": {Digest: "d2"}, "a": {Digest: "d1"}}, want)["NEO"].Hash
	if same != base {
		t.Errorf("hash changed with no content change: %s vs %s", same, base)
	}
	// a replaced image must be noticed
	replaced := BuildManifest(State{"a": {Digest: "CHANGED"}, "b": {Digest: "d2"}}, want)["NEO"].Hash
	if replaced == base {
		t.Error("hash unchanged after an image was replaced")
	}
	// so must a dropped one
	dropped := BuildManifest(State{"a": {Digest: "d1"}}, want)["NEO"].Hash
	if dropped == base {
		t.Error("hash unchanged after a set lost an image")
	}
}
