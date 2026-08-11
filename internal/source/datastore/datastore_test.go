package datastore

import (
	"context"
	"io"
	"log"
	"sort"
	"testing"

	"github.com/mtgban/go-mtgban/mtgmatcher"
	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/source"
)

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

// backendFixture mirrors how the Lorcana and Riftbound loaders shape a
// Backend: one set level Card per printing carrying the image, a CardObject
// per finish sharing it, and sealed products whose image lives on their card
// entry rather than on the product record.
func backendFixture() *mtgmatcher.Backend {
	base := mtgmatcher.Card{
		UUID:    "460",
		Name:    "Elsa",
		SetCode: "1",
		Images: map[string]string{
			"full":      "https://cdn.example.invalid/cards/elsa-460.png",
			"thumbnail": "https://cdn.example.invalid/thumbs/elsa-460.png",
		},
	}
	dashed := mtgmatcher.Card{
		UUID:    "ogn-066-298",
		Name:    "Yasuo",
		SetCode: "OGN",
		Images:  map[string]string{"full": "https://cdn.example.invalid/rb/yasuo.jpg"},
	}
	noImage := mtgmatcher.Card{UUID: "999", Name: "Imageless", SetCode: "1"}
	nilImages := mtgmatcher.Card{UUID: "998", Name: "NilMap", SetCode: "1", Images: nil}

	sealedCard := mtgmatcher.Card{
		UUID:    "1-600001",
		Name:    "The First Chapter Booster Box",
		SetCode: "1",
		Images:  map[string]string{"full": "https://cdn.example.invalid/sealed/box.jpg"},
	}

	return &mtgmatcher.Backend{
		Sets: map[string]*mtgmatcher.Set{
			"1": {
				Code:  "1",
				Name:  "The First Chapter",
				Cards: []mtgmatcher.Card{base, noImage, nilImages},
				SealedProduct: []mtgmatcher.SealedProduct{
					{UUID: "1-600001", Name: "Booster Box", SetCode: "1"},
				},
			},
			"OGN": {
				Code:  "OGN",
				Name:  "Origins",
				Cards: []mtgmatcher.Card{dashed},
			},
		},
		UUIDs: map[string]*mtgmatcher.CardObject{
			// per finish objects share the printing's image
			"460":      {Card: base},
			"460_f":    {Card: base, Foil: true},
			"1-600001": {Card: sealedCard, Sealed: true},
		},
	}
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

func TestBuildWantKeysSinglesByBaseUUID(t *testing.T) {
	want := buildFixture(t, nil)

	got, ok := want["460"]
	if !ok {
		t.Fatalf("want has no entry for base uuid 460; keys: %v", sortedKeys(want))
	}
	expect := mirror.Image{
		Key:        "460",
		URL:        "https://cdn.example.invalid/cards/elsa-460.png",
		ObjectPath: "singles/full/front/4/6/460.png",
		SetCode:    "1",
	}
	if got != expect {
		t.Errorf("want[460] = %+v, want %+v", got, expect)
	}

	// the finish uuids share the printing's image, so they must not each
	// become their own key and refetch the same bytes
	for _, k := range []string{"460_f", "460_rainbowpillars"} {
		if _, ok := want[k]; ok {
			t.Errorf("want unexpectedly contains finish uuid %q", k)
		}
	}
}

// Riftbound ids carry dashes and Lorcana set codes can be one character; both
// have to survive into a path rather than being rejected as unsafe.
func TestBuildWantHandlesDashedIDs(t *testing.T) {
	want := buildFixture(t, nil)

	got, ok := want["ogn-066-298"]
	if !ok {
		t.Fatalf("want has no entry for dashed uuid; keys: %v", sortedKeys(want))
	}
	if got.ObjectPath != "singles/full/front/o/g/ogn-066-298.jpg" {
		t.Errorf("ObjectPath = %q", got.ObjectPath)
	}
	if got.SetCode != "OGN" {
		t.Errorf("SetCode = %q, want OGN", got.SetCode)
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
		ObjectPath: "sealed/1/-/1-600001.jpg",
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
	for _, k := range []string{"999", "998"} {
		if _, ok := want[k]; ok {
			t.Errorf("want unexpectedly contains imageless card %q", k)
		}
	}
	if len(want) != 3 {
		t.Errorf("want has %d entries (%v), expected 3", len(want), sortedKeys(want))
	}
}

func TestBuildWantFilter(t *testing.T) {
	want := buildFixture(t, map[string]bool{"OGN": true})
	if _, ok := want["ogn-066-298"]; !ok {
		t.Error("filtered want should keep the OGN card")
	}
	if _, ok := want["460"]; ok {
		t.Error("filtered want should drop the set 1 card")
	}
}

// The extension travels with the image because these games are mirrored byte
// for byte from their own CDN, which does not have to serve webp like Scryfall.
func TestExtensionOf(t *testing.T) {
	for _, tt := range []struct {
		url  string
		want string
		ok   bool
	}{
		{"https://cdn.example.invalid/a/b.png", "png", true},
		{"https://cdn.example.invalid/a/b.JPG", "jpg", true},
		{"https://cdn.example.invalid/a/b.webp?v=3", "webp", true},
		{"https://cdn.example.invalid/a/b", "", false},
		{"https://cdn.example.invalid/a/b.", "", false},
		{"://nonsense", "", false},
	} {
		got, ok := extensionOf(tt.url)
		if ok != tt.ok || got != tt.want {
			t.Errorf("extensionOf(%q) = (%q, %v), want (%q, %v)", tt.url, got, ok, tt.want, tt.ok)
		}
	}
}

// An empty want-list means the datastore was not understood — a schema drift,
// or Lorcana singles not carrying a "full" key. Mirroring nothing would report
// success and quietly leave the game unmirrored, so it is an error instead.
func TestBuildWantRefusesAnEmptyResult(t *testing.T) {
	p := &Provider{game: source.Lorcana}
	empty := &mtgmatcher.Backend{Sets: map[string]*mtgmatcher.Set{
		"1": {Code: "1", Cards: []mtgmatcher.Card{{UUID: "1", SetCode: "1"}}},
	}}
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
	if !sealed.IsSealedKey("p-ogn-600001") || sealed.IsSealedKey("ogn-066-298") {
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
