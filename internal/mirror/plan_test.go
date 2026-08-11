package mirror

import (
	"reflect"
	"testing"
)

func planFixture() (State, map[string]Image) {
	state := State{
		"id-done":   {Digest: "d1", Source: "https://images.example.invalid/a.jpg"},
		"id-moved":  {Digest: "d2", Source: "https://images.example.invalid/old.jpg"},
		"id-orphan": {Digest: "d9", Source: "https://images.example.invalid/z.jpg"},
	}
	want := map[string]Image{
		"id-done":  {Key: "id-done", URL: "https://images.example.invalid/a.jpg", SetCode: "NEO"},
		"id-moved": {Key: "id-moved", URL: "https://images.example.invalid/new.jpg", SetCode: "NEO"},
		"id-new":   {Key: "id-new", URL: "https://images.example.invalid/b.jpg", SetCode: "NEO"},
		"p-SLX-1":  {Key: "p-SLX-1", URL: "https://product-images.tcgplayer.com/1.jpg", SetCode: "SLX"},
	}
	return state, want
}

func TestNeedFetch(t *testing.T) {
	state, want := planFixture()
	got := NeedFetch(state, want)
	wantKeys := []string{"id-moved", "id-new", "p-SLX-1"}
	if !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("NeedFetch = %v, want %v", got, wantKeys)
	}
}

func TestSetDigestsSkipsUnfetched(t *testing.T) {
	state, want := planFixture()
	got := SetDigests(state, want)
	wantMap := map[string]map[string]string{
		"NEO": {"id-done": "d1", "id-moved": "d2"},
	}
	if !reflect.DeepEqual(got, wantMap) {
		t.Errorf("SetDigests = %v, want %v", got, wantMap)
	}
}

func TestBundlesToRebuild(t *testing.T) {
	digests := map[string]map[string]string{
		"NEO": {"7673784e-db4b-43a1-8d55-1bb9fc1e284f": "d1"},
		"MID": {"bd8fa327-dd41-4737-8f19-2cf5eb1f7cdd": "d2"},
		"VOW": {"d27cf7b7-7982-46bd-a559-7789c0e74bae": "d3"},
	}
	m := Manifest{
		"NEO": {Hash: BundleHash(digests["NEO"]), Count: 1, Bytes: 10},
		"MID": {Hash: "stale", Count: 1, Bytes: 10},
	}
	got := BundlesToRebuild(m, digests)
	want := []string{"MID", "VOW"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BundlesToRebuild = %v, want %v", got, want)
	}
}

func TestDomains(t *testing.T) {
	_, want := planFixture()
	got := Domains(want)
	wantMap := map[string]int{
		"images.example.invalid":       3,
		"product-images.tcgplayer.com": 1,
	}
	if !reflect.DeepEqual(got, wantMap) {
		t.Errorf("Domains = %v, want %v", got, wantMap)
	}
}
