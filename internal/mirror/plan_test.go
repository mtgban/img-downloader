package mirror

import (
	"reflect"
	"testing"

	"github.com/mtgban/img-downloader/internal/mtgjson"
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

func buildWantFixture() []mtgjson.SetImages {
	return []mtgjson.SetImages{
		{
			Code:        "NEO",
			ScryfallIDs: []string{"7673784e-db4b-43a1-8d55-1bb9fc1e284f", "bd8fa327-dd41-4737-8f19-2cf5eb1f7cdd", "0050b693-7bad-4c0c-baca-0186d153ce2e"},
			Sealed:      []mtgjson.SealedRef{{TcgplayerProductID: "111"}},
		},
		{
			Code:        "MID",
			ScryfallIDs: []string{"d27cf7b7-7982-46bd-a559-7789c0e74bae"},
		},
	}
}

func buildWantScryURL() map[string]string {
	return map[string]string{
		"7673784e-db4b-43a1-8d55-1bb9fc1e284f": "https://cards.scryfall.io/normal/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.jpg?1600000000",
		"bd8fa327-dd41-4737-8f19-2cf5eb1f7cdd": "https://cards.scryfall.io/normal/front/b/d/bd8fa327-dd41-4737-8f19-2cf5eb1f7cdd.jpg?1600000000",
		"d27cf7b7-7982-46bd-a559-7789c0e74bae": "https://cards.scryfall.io/normal/front/d/2/d27cf7b7-7982-46bd-a559-7789c0e74bae.jpg?1600000000",
	}
}

func TestBuildWantSingles(t *testing.T) {
	want, missing, _ := BuildWant(buildWantFixture(), buildWantScryURL(), nil)

	gotA := want["7673784e-db4b-43a1-8d55-1bb9fc1e284f"]
	wantA := Image{
		Key:        "7673784e-db4b-43a1-8d55-1bb9fc1e284f",
		URL:        "https://cards.scryfall.io/normal/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.jpg?1600000000",
		ObjectPath: "singles/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.jpg",
		SetCode:    "NEO",
	}
	if gotA != wantA {
		t.Errorf("want[7673784e-db4b-43a1-8d55-1bb9fc1e284f] = %+v, want %+v", gotA, wantA)
	}

	if _, ok := want["d27cf7b7-7982-46bd-a559-7789c0e74bae"]; !ok {
		t.Errorf("want missing d27cf7b7-7982-46bd-a559-7789c0e74bae from unfiltered MID set")
	}

	wantMissing := []string{"0050b693-7bad-4c0c-baca-0186d153ce2e"}
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Errorf("missing = %v, want %v", missing, wantMissing)
	}
}

func TestBuildWantSealed(t *testing.T) {
	want, _, _ := BuildWant(buildWantFixture(), buildWantScryURL(), nil)

	got, ok := want["p-NEO-111"]
	if !ok {
		t.Fatal("want missing p-NEO-111 sealed entry")
	}
	wantImg := Image{
		Key:        "p-NEO-111",
		URL:        "https://product-images.tcgplayer.com/111.jpg",
		ObjectPath: "sealed/NEO/111.jpg",
		SetCode:    "NEO",
	}
	if got != wantImg {
		t.Errorf("want[p-NEO-111] = %+v, want %+v", got, wantImg)
	}
}

func TestBuildWantSkipsInvalidSealed(t *testing.T) {
	sets := []mtgjson.SetImages{
		{
			Code:        "NEO",
			ScryfallIDs: []string{"7673784e-db4b-43a1-8d55-1bb9fc1e284f"},
			Sealed: []mtgjson.SealedRef{
				{TcgplayerProductID: "111"},
				{TcgplayerProductID: "abc"},
				{TcgplayerProductID: ""},
			},
		},
	}
	scryURL := map[string]string{
		"7673784e-db4b-43a1-8d55-1bb9fc1e284f": "https://cards.scryfall.io/normal/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.jpg?1600000000",
	}

	want, _, invalidSealed := BuildWant(sets, scryURL, nil)

	if _, ok := want["p-NEO-111"]; !ok {
		t.Error("want missing valid sealed entry p-NEO-111")
	}
	if _, ok := want["p-NEO-abc"]; ok {
		t.Error("want should not include non-numeric tcgId p-NEO-abc")
	}
	if invalidSealed != 2 {
		t.Errorf("invalidSealed = %d, want 2", invalidSealed)
	}
}

func TestBuildWantFilter(t *testing.T) {
	filter := map[string]bool{"NEO": true}
	want, missing, _ := BuildWant(buildWantFixture(), buildWantScryURL(), filter)

	if _, ok := want["d27cf7b7-7982-46bd-a559-7789c0e74bae"]; ok {
		t.Errorf("filtered MID set should not contribute d27cf7b7-7982-46bd-a559-7789c0e74bae")
	}
	if _, ok := want["7673784e-db4b-43a1-8d55-1bb9fc1e284f"]; !ok {
		t.Errorf("filtered want should still include NEO 7673784e-db4b-43a1-8d55-1bb9fc1e284f")
	}
	wantMissing := []string{"0050b693-7bad-4c0c-baca-0186d153ce2e"}
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Errorf("missing = %v, want %v", missing, wantMissing)
	}
}

func TestBuildWantURLChangeTriggersNeedFetch(t *testing.T) {
	state := State{
		"7673784e-db4b-43a1-8d55-1bb9fc1e284f": {Digest: "d1", Source: "https://cards.scryfall.io/normal/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.jpg?1500000000"},
	}
	scryURL := buildWantScryURL()
	want, _, _ := BuildWant(buildWantFixture(), scryURL, map[string]bool{"NEO": true})

	got := NeedFetch(state, want)
	found := false
	for _, k := range got {
		if k == "7673784e-db4b-43a1-8d55-1bb9fc1e284f" {
			found = true
		}
	}
	if !found {
		t.Errorf("NeedFetch = %v, want 7673784e-db4b-43a1-8d55-1bb9fc1e284f present after URL timestamp change", got)
	}
}
