package mirror

import (
	"reflect"
	"testing"

	"github.com/the-muppet2/img-downloader/internal/mtgjson"
)

func planFixture() (State, map[string]Image) {
	state := State{
		"id-done":   {Digest: "d1", Source: "https://cards.scryfall.io/a.jpg"},
		"id-moved":  {Digest: "d2", Source: "https://cards.scryfall.io/old.jpg"},
		"id-orphan": {Digest: "d9", Source: "https://cards.scryfall.io/z.jpg"},
	}
	want := map[string]Image{
		"id-done":  {Key: "id-done", URL: "https://cards.scryfall.io/a.jpg", SetCode: "NEO"},
		"id-moved": {Key: "id-moved", URL: "https://cards.scryfall.io/new.jpg", SetCode: "NEO"},
		"id-new":   {Key: "id-new", URL: "https://cards.scryfall.io/b.jpg", SetCode: "NEO"},
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
		"NEO": {"id-a": "d1"},
		"MID": {"id-b": "d2"},
		"VOW": {"id-c": "d3"},
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
		"cards.scryfall.io":            3,
		"product-images.tcgplayer.com": 1,
	}
	if !reflect.DeepEqual(got, wantMap) {
		t.Errorf("Domains = %v, want %v", got, wantMap)
	}
}

func buildWantFixture() []mtgjson.SetImages {
	return []mtgjson.SetImages{
		{
			Code:        "NEO",
			ScryfallIDs: []string{"id-a", "id-b", "id-missing"},
			Sealed:      []mtgjson.SealedRef{{TcgplayerProductID: "111"}},
		},
		{
			Code:        "MID",
			ScryfallIDs: []string{"id-c"},
		},
	}
}

func buildWantScryURL() map[string]string {
	return map[string]string{
		"id-a": "https://cards.scryfall.io/normal/front/a/id-a.jpg?1600000000",
		"id-b": "https://cards.scryfall.io/normal/front/b/id-b.jpg?1600000000",
		"id-c": "https://cards.scryfall.io/normal/front/c/id-c.jpg?1600000000",
	}
}

func TestBuildWantSingles(t *testing.T) {
	want, missing := BuildWant(buildWantFixture(), buildWantScryURL(), nil)

	gotA := want["id-a"]
	wantA := Image{
		Key:        "id-a",
		URL:        "https://cards.scryfall.io/normal/front/a/id-a.jpg?1600000000",
		ObjectPath: "normal/front/a/id-a.jpg",
		SetCode:    "NEO",
	}
	if gotA != wantA {
		t.Errorf("want[id-a] = %+v, want %+v", gotA, wantA)
	}

	if _, ok := want["id-c"]; !ok {
		t.Errorf("want missing id-c from unfiltered MID set")
	}

	wantMissing := []string{"id-missing"}
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Errorf("missing = %v, want %v", missing, wantMissing)
	}
}

func TestBuildWantSealed(t *testing.T) {
	want, _ := BuildWant(buildWantFixture(), buildWantScryURL(), nil)

	got, ok := want["p-NEO-111"]
	if !ok {
		t.Fatal("want missing p-NEO-111 sealed entry")
	}
	wantImg := Image{
		Key:        "p-NEO-111",
		URL:        "https://product-images.tcgplayer.com/111.jpg",
		ObjectPath: "NEO/sealed/111.jpg",
		SetCode:    "NEO",
	}
	if got != wantImg {
		t.Errorf("want[p-NEO-111] = %+v, want %+v", got, wantImg)
	}
}

func TestBuildWantFilter(t *testing.T) {
	filter := map[string]bool{"NEO": true}
	want, missing := BuildWant(buildWantFixture(), buildWantScryURL(), filter)

	if _, ok := want["id-c"]; ok {
		t.Errorf("filtered MID set should not contribute id-c")
	}
	if _, ok := want["id-a"]; !ok {
		t.Errorf("filtered want should still include NEO id-a")
	}
	wantMissing := []string{"id-missing"}
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Errorf("missing = %v, want %v", missing, wantMissing)
	}
}

func TestBuildWantURLChangeTriggersNeedFetch(t *testing.T) {
	state := State{
		"id-a": {Digest: "d1", Source: "https://cards.scryfall.io/normal/front/a/id-a.jpg?1500000000"},
	}
	scryURL := buildWantScryURL()
	want, _ := BuildWant(buildWantFixture(), scryURL, map[string]bool{"NEO": true})

	got := NeedFetch(state, want)
	found := false
	for _, k := range got {
		if k == "id-a" {
			found = true
		}
	}
	if !found {
		t.Errorf("NeedFetch = %v, want id-a present after URL timestamp change", got)
	}
}
