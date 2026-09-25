package datastore

import (
	"bytes"
	"context"
	"io"
	"log"
	"sort"
	"strings"
	"testing"

	"github.com/mtgban/go-mtgban/mtgmatcher"
	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/source"
)

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

// productEntry is one finish of a TCGplayer product, the way every loader
// stores it: an entry of its own carrying the product's id and image.
func productEntry(uuid, setCode, productID, image string) *mtgmatcher.CardObject {
	return &mtgmatcher.CardObject{Card: mtgmatcher.Card{
		UUID:        uuid,
		SetCode:     setCode,
		Identifiers: map[string]string{"tcgplayerProductId": productID},
		Images:      map[string]string{"full": image},
	}}
}

// backendFixture mirrors how the loaders shape a Backend: an entry per finish,
// listed in AllUUIDs the way the website's catalog walks them, and sealed
// products whose image lives on their card entry rather than on the product
// record. The rows are real: DTD011 is two Flesh and Blood products, 502592
// sold in Normal and Rainbow Foil and 502740 in Cold Foil; unl-t01 is a
// Riftbound token that names no product and is sold in two finishes; and "1"
// is a Lorcana set code one character long.
func backendFixture() *mtgmatcher.Backend {
	token := mtgmatcher.Card{
		SetCode:   "UNL",
		FoilUUIDs: map[string]string{"nonfoil": "unl-t01", "foil": "unl-t01_foil"},
		Images:    map[string]string{"full": "https://cdn.example.invalid/rb/unl-t01.png"},
	}
	tokenFoil := token
	token.UUID, tokenFoil.UUID = "unl-t01", "unl-t01_foil"

	sealedCard := mtgmatcher.Card{
		UUID:    "1-600001",
		Name:    "The First Chapter Booster Box",
		SetCode: "1",
		Images:  map[string]string{"full": "https://cdn.example.invalid/sealed/box.jpg"},
	}

	uuids := map[string]*mtgmatcher.CardObject{
		"dtd011_502592":             productEntry("dtd011_502592", "DTD", "502592", "https://cdn.example.invalid/502592.jpg"),
		"dtd011_502592_rainbowfoil": productEntry("dtd011_502592_rainbowfoil", "DTD", "502592", "https://cdn.example.invalid/502592.jpg"),
		"dtd011_502740_coldfoil":    productEntry("dtd011_502740_coldfoil", "DTD", "502740", "https://cdn.example.invalid/502740.jpg"),
		"1":                         productEntry("1", "1", "494102", "https://cdn.example.invalid/elsa.jpg"),
		"1_foil":                    productEntry("1_foil", "1", "494102", "https://cdn.example.invalid/elsa.jpg"),
		"unl-t01":                   {Card: token},
		"unl-t01_foil":              {Card: tokenFoil},
		"999": {Card: mtgmatcher.Card{UUID: "999", SetCode: "1",
			Identifiers: map[string]string{"tcgplayerProductId": "999999"},
			Images:      map[string]string{"thumbnail": "https://cdn.example.invalid/t.jpg"}}},
		"998":      {Card: mtgmatcher.Card{UUID: "998", SetCode: "1", Images: nil}},
		"1-600001": {Card: sealedCard, Sealed: true},
	}
	backend := &mtgmatcher.Backend{
		Sets: map[string]*mtgmatcher.Set{
			"1": {
				Code: "1",
				Name: "The First Chapter",
				SealedProduct: []mtgmatcher.SealedProduct{
					{UUID: "1-600001", Name: "Booster Box", SetCode: "1"},
				},
			},
			"DTD": {Code: "DTD", Name: "Dusk till Dawn"},
			"UNL": {Code: "UNL", Name: "Unleashed"},
		},
		UUIDs: uuids,
	}
	// in map order on purpose: the loaders do not sort it either
	for uuid, co := range uuids {
		if !co.Sealed {
			backend.AllUUIDs = append(backend.AllUUIDs, uuid)
		}
	}
	return backend
}

func buildFixture(t *testing.T, filter map[string]bool) source.Want {
	t.Helper()
	p := &Provider{game: source.Lorcana}
	want, err := p.wantFromBackend(backendFixture(), filter, discardLog())
	if err != nil {
		t.Fatalf("wantFromBackend: %v", err)
	}
	return want
}

// Every finish of a product shares its image, filed once under the product
// id, which is the key the website's catalog asks for (internal/offlineapi,
// datastoreImageKey). A finish's uuid, or the collector number two products
// share, must never become one.
func TestBuildWantKeysSinglesByProduct(t *testing.T) {
	want := buildFixture(t, nil)

	got, ok := want["502592"]
	if !ok {
		t.Fatalf("want has no entry for product 502592; keys: %v", sortedKeys(want))
	}
	expect := mirror.Image{
		Key:        "502592",
		URL:        "https://cdn.example.invalid/502592.jpg",
		ObjectPath: "singles/full/front/5/0/502592.webp",
		SetCode:    "DTD",
	}
	if got != expect {
		t.Errorf("want[502592] = %+v, want %+v", got, expect)
	}
	if got := want["502740"].URL; got != "https://cdn.example.invalid/502740.jpg" {
		t.Errorf("want[502740].URL = %q, want the Cold Foil product's own image", got)
	}

	for _, k := range []string{"dtd011", "dtd011_502592", "dtd011_502592_rainbowfoil", "dtd011_502740_coldfoil"} {
		if _, ok := want[k]; ok {
			t.Errorf("want unexpectedly contains %q", k)
		}
	}
}

// A card that names no product still has one image for all its finishes, so
// the token's two are filed once, under the key they share. That key carries a
// dash and an underscore, and both have to survive into a path.
func TestBuildWantFoldsAPrintingThatNamesNoProduct(t *testing.T) {
	want := buildFixture(t, nil)

	got, ok := want["unl-t01_foil"]
	if !ok {
		t.Fatalf("want has no entry for the token; keys: %v", sortedKeys(want))
	}
	if got.ObjectPath != "singles/full/front/u/n/unl-t01_foil.webp" {
		t.Errorf("ObjectPath = %q", got.ObjectPath)
	}
	if got.SetCode != "UNL" {
		t.Errorf("SetCode = %q, want UNL", got.SetCode)
	}
	if _, ok := want["unl-t01"]; ok {
		t.Error("want files the token's nonfoil finish a second time")
	}
}

// Lorcana set codes can be one character, and the set code names the manifest
// entry and the bundle, so it has to survive rather than be rejected as unsafe.
func TestBuildWantKeepsAOneCharacterSetCode(t *testing.T) {
	want := buildFixture(t, nil)
	if got := want["494102"].SetCode; got != "1" {
		t.Errorf("want[494102].SetCode = %q, want 1", got)
	}
}

func TestBuildWantSealedKeyIsSelfDescribing(t *testing.T) {
	want := buildFixture(t, nil)

	got, ok := want["p-1-600001"]
	if !ok {
		t.Fatalf("want has no sealed entry; keys: %v", sortedKeys(want))
	}
	expect := mirror.Image{
		Key:        "p-1-600001",
		URL:        "https://cdn.example.invalid/sealed/box.jpg",
		ObjectPath: "sealed/1/1-600001.webp",
		SetCode:    "1",
	}
	if got != expect {
		t.Errorf("want[p-1-600001] = %+v, want %+v", got, expect)
	}
	if !mirror.IsSealedKey(got.Key) {
		t.Errorf("IsSealedKey(%q) = false, want true", got.Key)
	}
}

// A card with no usable image is skipped rather than mirrored as a broken
// entry, and a nil Images map — which Lorcana singles can have, since the
// loader passes LorcanaJSON's map straight through — must not panic.
func TestBuildWantSkipsCardsWithoutAnImage(t *testing.T) {
	want := buildFixture(t, nil)
	for _, k := range []string{"999", "999999", "998"} {
		if _, ok := want[k]; ok {
			t.Errorf("want unexpectedly contains imageless card %q", k)
		}
	}
	if len(want) != 5 {
		t.Errorf("want has %d entries (%v), expected 5", len(want), sortedKeys(want))
	}
}

func TestBuildWantFilter(t *testing.T) {
	want := buildFixture(t, map[string]bool{"UNL": true})
	if _, ok := want["unl-t01_foil"]; !ok {
		t.Error("filtered want should keep the UNL card")
	}
	for _, k := range []string{"502592", "494102", "p-1-600001"} {
		if _, ok := want[k]; ok {
			t.Errorf("filtered want should drop %q", k)
		}
	}
}

// Two entries claiming one key with different images leave one of them
// showing the other's picture. Which one wins must not follow the order the
// loader listed them in, or every run would refetch the key, and the clash is
// logged rather than passed over.
func TestBuildWantResolvesAClashTheSameWayEveryRun(t *testing.T) {
	a := productEntry("a_700000", "DTD", "700000", "https://cdn.example.invalid/a.jpg")
	b := productEntry("b_700000_foil", "DTD", "700000", "https://cdn.example.invalid/b.jpg")
	for _, order := range [][]string{{"a_700000", "b_700000_foil"}, {"b_700000_foil", "a_700000"}} {
		backend := &mtgmatcher.Backend{
			Sets:     map[string]*mtgmatcher.Set{"DTD": {Code: "DTD"}},
			UUIDs:    map[string]*mtgmatcher.CardObject{"a_700000": a, "b_700000_foil": b},
			AllUUIDs: order,
		}
		var logged bytes.Buffer
		p := &Provider{game: source.Lorcana}
		want, err := p.wantFromBackend(backend, nil, log.New(&logged, "", 0))
		if err != nil {
			t.Fatal(err)
		}
		if got := want["700000"].URL; got != "https://cdn.example.invalid/a.jpg" {
			t.Errorf("listed as %v, want[700000].URL = %q, want the first uuid's", order, got)
		}
		if !strings.Contains(logged.String(), "b_700000_foil") {
			t.Errorf("listed as %v, the clash went unlogged:\n%s", order, logged.String())
		}
	}
}

// An empty want-list means the datastore was not understood — a schema drift,
// or Lorcana singles not carrying a "full" key. Mirroring nothing would report
// success and quietly leave the game unmirrored, so it is an error instead.
func TestBuildWantRefusesAnEmptyResult(t *testing.T) {
	p := &Provider{game: source.Lorcana}
	empty := &mtgmatcher.Backend{
		Sets:     map[string]*mtgmatcher.Set{"1": {Code: "1"}},
		UUIDs:    map[string]*mtgmatcher.CardObject{"1": {Card: mtgmatcher.Card{UUID: "1", SetCode: "1"}}},
		AllUUIDs: []string{"1"},
	}
	if _, err := p.wantFromBackend(empty, nil, discardLog()); err == nil {
		t.Fatal("wantFromBackend on an imageless datastore = nil error, want a refusal")
	}
}

func TestNewRejectsUnregisteredGameAndMissingConfig(t *testing.T) {
	// Magic has its own provider and is deliberately not blank-imported here
	if _, err := New(source.Magic, Config{Bucket: nil, Path: "x"}); err == nil {
		t.Error("New with a nil bucket = nil error, want an error")
	}
	if _, err := New(source.Lorcana, Config{Bucket: stubBucket{}, Path: ""}); err == nil {
		t.Error("New with no datastore path = nil error, want an error")
	}
	if _, err := New(source.Lorcana, Config{Bucket: stubBucket{}, Path: "x"}); err != nil {
		t.Errorf("New(lorcana) = %v, want success", err)
	}
}

// Registering the game packages is what makes Open resolve a name, so a
// missing blank import must show up as a test failure rather than at runtime.
func TestGamePackagesAreRegistered(t *testing.T) {
	registered := mtgmatcher.RegisteredGames()
	for _, game := range []source.Game{source.Lorcana, source.Riftbound} {
		if !slicesContains(registered, string(game)) {
			t.Errorf("mtgmatcher has no loader registered for %q; registered: %v", game, registered)
		}
	}
}

func TestProviderImplementsSourceInterfaces(t *testing.T) {
	p, err := New(source.Riftbound, Config{Bucket: stubBucket{}, Path: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var _ source.Provider = p
	if p.Game() != source.Riftbound {
		t.Errorf("Game() = %q", p.Game())
	}
	sealed, ok := any(p).(source.SealedAware)
	if !ok {
		t.Fatal("datastore.Provider does not implement source.SealedAware")
	}
	if !sealed.IsSealedKey("p-ogn-600001") || sealed.IsSealedKey("652968") {
		t.Error("IsSealedKey did not separate sealed from singles")
	}
}

type stubBucket struct{}

func (stubBucket) NewReader(context.Context, string) (io.ReadCloser, error) { panic("unused") }

func sortedKeys(want source.Want) []string {
	out := make([]string, 0, len(want))
	for k := range want {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
