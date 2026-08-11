package magic_test

import (
	"reflect"
	"testing"

	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/mtgjson"
	"github.com/mtgban/img-downloader/internal/source"
	"github.com/mtgban/img-downloader/internal/source/magic"
)

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
		"7673784e-db4b-43a1-8d55-1bb9fc1e284f": "https://cards.scryfall.io/grid/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.webp?1600000000",
		"bd8fa327-dd41-4737-8f19-2cf5eb1f7cdd": "https://cards.scryfall.io/grid/front/b/d/bd8fa327-dd41-4737-8f19-2cf5eb1f7cdd.webp?1600000000",
		"d27cf7b7-7982-46bd-a559-7789c0e74bae": "https://cards.scryfall.io/grid/front/d/2/d27cf7b7-7982-46bd-a559-7789c0e74bae.webp?1600000000",
	}
}

func TestBuildWantSingles(t *testing.T) {
	want, missing, _ := magic.BuildWant(buildWantFixture(), buildWantScryURL(), nil)

	gotA := want["7673784e-db4b-43a1-8d55-1bb9fc1e284f"]
	wantA := mirror.Image{
		Key:        "7673784e-db4b-43a1-8d55-1bb9fc1e284f",
		URL:        "https://cards.scryfall.io/grid/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.webp?1600000000",
		ObjectPath: "singles/grid/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.webp",
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
	want, _, _ := magic.BuildWant(buildWantFixture(), buildWantScryURL(), nil)

	got, ok := want["p-NEO-111"]
	if !ok {
		t.Fatal("want missing p-NEO-111 sealed entry")
	}
	wantImg := mirror.Image{
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
		"7673784e-db4b-43a1-8d55-1bb9fc1e284f": "https://cards.scryfall.io/grid/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.webp?1600000000",
	}

	want, _, invalidSealed := magic.BuildWant(sets, scryURL, nil)

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
	want, missing, _ := magic.BuildWant(buildWantFixture(), buildWantScryURL(), filter)

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
	state := mirror.State{
		"7673784e-db4b-43a1-8d55-1bb9fc1e284f": {Digest: "d1", Source: "https://cards.scryfall.io/grid/front/7/6/7673784e-db4b-43a1-8d55-1bb9fc1e284f.webp?1500000000"},
	}
	scryURL := buildWantScryURL()
	want, _, _ := magic.BuildWant(buildWantFixture(), scryURL, map[string]bool{"NEO": true})

	got := mirror.NeedFetch(state, want)
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

// The Magic provider is the one game that must keep resuming against the
// existing mirror-state.json, so the identity the mirror keys on — the image
// key and its source URL — is pinned here rather than left to the object-path
// tests.
func TestProviderIsMagicAndSealedAware(t *testing.T) {
	var p source.Provider = &magic.Provider{}
	if p.Game() != source.Magic {
		t.Errorf("Game() = %q, want %q", p.Game(), source.Magic)
	}
	sealed, ok := p.(source.SealedAware)
	if !ok {
		t.Fatal("magic.Provider does not implement source.SealedAware")
	}
	if !sealed.IsSealedKey("p-NEO-111") {
		t.Error("IsSealedKey(p-NEO-111) = false, want true")
	}
	if sealed.IsSealedKey("7673784e-db4b-43a1-8d55-1bb9fc1e284f") {
		t.Error("IsSealedKey(scryfall id) = true, want false")
	}
}
