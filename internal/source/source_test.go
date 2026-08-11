package source_test

import (
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
	for _, bad := range []string{"", "mtg", "pokemon", "magic/../lorcana", "magic lorcana"} {
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
