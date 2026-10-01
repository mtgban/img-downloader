package mirror

import (
	"maps"
	"slices"
	"testing"
)

func planFixture() (State, map[string]Image) {
	state := State{
		"id-done":   {Digest: "d1", Source: "https://images.example.invalid/a.jpg", ObjectPath: "singles/grid/front/i/d/id-done.webp"},
		"id-moved":  {Digest: "d2", Source: "https://images.example.invalid/old.jpg", ObjectPath: "singles/grid/front/i/d/id-moved.webp"},
		"id-orphan": {Digest: "d9", Source: "https://images.example.invalid/z.jpg", ObjectPath: "singles/grid/front/i/d/id-orphan.webp"},
	}
	want := map[string]Image{
		"id-done":  {Key: "id-done", URL: "https://images.example.invalid/a.jpg", ObjectPath: "singles/grid/front/i/d/id-done.webp", SetCode: "NEO"},
		"id-moved": {Key: "id-moved", URL: "https://images.example.invalid/new.jpg", ObjectPath: "singles/grid/front/i/d/id-moved.webp", SetCode: "NEO"},
		"id-new":   {Key: "id-new", URL: "https://images.example.invalid/b.jpg", ObjectPath: "singles/grid/front/i/d/id-new.webp", SetCode: "NEO"},
		"p-SLX-1":  {Key: "p-SLX-1", URL: "https://product-images.tcgplayer.com/1.jpg", ObjectPath: "sealed/SLX/1.webp", SetCode: "SLX"},
	}
	return state, want
}

func TestNeedFetch(t *testing.T) {
	state, want := planFixture()
	got := NeedFetch(state, want)
	wantKeys := []string{"id-moved", "id-new", "p-SLX-1"}
	if !slices.Equal(got, wantKeys) {
		t.Errorf("NeedFetch = %v, want %v", got, wantKeys)
	}
}

func TestSetDigestsSkipsUnfetched(t *testing.T) {
	state, want := planFixture()
	got := SetDigests(state, want)
	wantMap := map[string]map[string]string{
		"NEO": {"id-done": "d1", "id-moved": "d2"},
	}
	if !maps.EqualFunc(got, wantMap, maps.Equal) {
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
	if !slices.Equal(got, want) {
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
	if !maps.Equal(got, wantMap) {
		t.Errorf("Domains = %v, want %v", got, wantMap)
	}
}

// Converting the corpus to webp moved every object the mirror had stored in
// its source's own format, without changing one url to fetch it from. A diff
// that only compared sources would call all of that up to date and leave the
// bundles pointing at objects nobody ever wrote.
func TestNeedFetchSeesAnObjectThatMoved(t *testing.T) {
	const src = "https://product-images.tcgplayer.com/1.jpg"
	state := State{
		"p-SLX-1": {Digest: "d1", Source: src, ObjectPath: "sealed/SLX/1.jpg"},
	}
	want := map[string]Image{
		"p-SLX-1": {Key: "p-SLX-1", URL: src, ObjectPath: "sealed/SLX/1.webp", SetCode: "SLX"},
	}
	if got := NeedFetch(state, want); !slices.Equal(got, []string{"p-SLX-1"}) {
		t.Errorf("NeedFetch = %v, want the moved image refetched", got)
	}

	// and once it has been, it stays put
	state["p-SLX-1"] = StateEntry{Digest: "d2", Source: src, ObjectPath: "sealed/SLX/1.webp"}
	if got := NeedFetch(state, want); len(got) != 0 {
		t.Errorf("NeedFetch = %v, want nothing once the object is where it belongs", got)
	}
}

// An entry that records no path cannot say where its object is, so it is
// fetched once more and records one.
func TestNeedFetchRefetchesEntriesWithNoObjectPath(t *testing.T) {
	const scry = "https://cards.scryfall.io/grid/front/a/b/single.webp?123"
	state := State{"single": {Digest: "d1", Source: scry}}
	want := map[string]Image{
		"single": {Key: "single", URL: scry, ObjectPath: "singles/grid/front/a/b/single.webp", SetCode: "NEO"},
	}
	if got := NeedFetch(state, want); !slices.Equal(got, []string{"single"}) {
		t.Errorf("NeedFetch = %v, want an entry with no recorded path refetched", got)
	}
}

// A missing marker records that a source had no image, not an object on disk,
// so there is nothing to move and RetryMissing stays the only thing that asks
// again. Treating it as misfiled would refetch every unpublished image on
// every run, which is the loop that marker exists to stop.
func TestNeedFetchLeavesMissingMarkersAlone(t *testing.T) {
	state := State{
		"p-SLX-1": {Source: "https://product-images.tcgplayer.com/1.jpg", Missing: true},
	}
	want := map[string]Image{
		"p-SLX-1": {Key: "p-SLX-1", URL: "https://product-images.tcgplayer.com/1.jpg", ObjectPath: "sealed/SLX/1.webp", SetCode: "SLX"},
	}
	if got := NeedFetch(state, want); len(got) != 0 {
		t.Errorf("NeedFetch = %v, want the missing marker left alone", got)
	}
}
