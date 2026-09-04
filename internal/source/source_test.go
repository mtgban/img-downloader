package source_test

import (
	"slices"

	"github.com/mtgban/go-mtgban/mtgmatcher"
	"reflect"
	"testing"

	"github.com/mtgban/img-downloader/internal/mirror"
	"github.com/mtgban/img-downloader/internal/source"
)

func TestParseGame(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want source.Game
	}{
		{"magic", source.Magic},
		{"MAGIC", source.Magic},
		{" lorcana ", source.Lorcana},
		{"Riftbound", source.Riftbound},
	} {
		got, err := source.ParseGame(tt.in)
		if err != nil {
			t.Errorf("ParseGame(%q) error = %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseGame(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The game names the bucket prefix written to, so a value that is nearly right
// must be refused rather than resolved to something plausible.
func TestParseGameRejectsUnknown(t *testing.T) {
	// Not a near-miss like "pokemon", which this test used until pokemon
	// became a game the mirror runs: a name that is never going to be one.
	for _, bad := range []string{"", "mtg", "notagame", "magic/../lorcana", "magic lorcana"} {
		if got, err := source.ParseGame(bad); err == nil {
			t.Errorf("ParseGame(%q) = %q, want an error", bad, got)
		}
	}
}

// Every game listed must be a plain lowercase token, because it becomes a
// path segment at the bucket base.
func TestGamesArePathSafe(t *testing.T) {
	for _, g := range source.Games() {
		for _, r := range string(g) {
			if r < 'a' || r > 'z' {
				t.Errorf("game %q contains %q, want lowercase letters only", g, r)
			}
		}
	}
}

func TestSortedSetCodes(t *testing.T) {
	want := source.Want{
		"a": {Key: "a", SetCode: "NEO"},
		"b": {Key: "b", SetCode: "MID"},
		"c": {Key: "c", SetCode: "NEO"},
		"d": {Key: "d"},
	}
	got := source.SortedSetCodes(want)
	if !reflect.DeepEqual(got, []string{"MID", "NEO"}) {
		t.Errorf("SortedSetCodes = %v, want [MID NEO]", got)
	}
}

// Want must stay assignable to what the mirror consumes, so a provider's
// result can be handed straight to mirror.Run without a copy.
func TestWantIsAMirrorImageMap(t *testing.T) {
	want := source.Want{"a": {Key: "a", URL: "https://example.invalid/a.webp"}}
	var plain map[string]mirror.Image = want
	if plain["a"].Key != "a" {
		t.Errorf("plain[a] = %+v", plain["a"])
	}
	if got := mirror.NeedFetch(mirror.State{}, want); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("NeedFetch(want) = %v, want [a]", got)
	}
}

// Games is only as good as the blank import that fills it: drop that and it
// returns nothing, ParseGame refuses every name, and the tool mirrors no game
// at all while compiling perfectly.
func TestGamesIsNotEmpty(t *testing.T) {
	games := source.Games()
	if len(games) == 0 {
		t.Fatal("Games() is empty, so no loader is registered and no game can be mirrored")
	}
	// magic and lorcana are the two the rest of the suite leans on
	for _, want := range []source.Game{source.Magic, source.Lorcana} {
		if !slices.Contains(games, want) {
			t.Errorf("Games() = %v, missing %q", games, want)
		}
	}
}

// Whatever mtgmatcher registers is what the tool offers, so a game added
// upstream arrives with the dependency rather than with an edit here.
func TestGamesFollowsTheRegistry(t *testing.T) {
	registered := mtgmatcher.RegisteredGames()
	if len(source.Games()) != len(registered) {
		t.Errorf("Games() has %d entries, mtgmatcher registers %d: %v vs %v",
			len(source.Games()), len(registered), source.Games(), registered)
	}
	for _, name := range registered {
		if !slices.Contains(source.Games(), source.Game(name)) {
			t.Errorf("mtgmatcher registers %q but Games() omits it", name)
		}
	}
}
