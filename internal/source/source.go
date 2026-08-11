// Package source defines the seam between a game's card data and the mirror.
//
// The mirror itself is game-agnostic: it takes a want-list of images, diffs it
// against state, fetches what changed, and writes objects. Everything that is
// specific to a game — where the card list comes from, what an image URL looks
// like, how an id becomes an object path — lives behind Provider.
package source

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/mtgban/img-downloader/internal/mirror"
)

// Game names one card game the mirror can run against. It is also the bucket
// prefix each game's objects, state, and manifest live under, so it must stay
// a plain lowercase token.
type Game string

const (
	// Magic is the original, and only, game sourced from public data
	// (MTGJSON plus Scryfall) rather than from mtgban's own datastore.
	Magic Game = "magic"
	// Lorcana is sourced from mtgban's datastore.
	Lorcana Game = "lorcana"
	// Riftbound is sourced from mtgban's datastore.
	Riftbound Game = "riftbound"
)

// Games lists every game the tool knows how to mirror, in a stable order.
func Games() []Game { return []Game{Magic, Lorcana, Riftbound} }

// ParseGame resolves a flag or env value into a known Game. It is
// case-insensitive and tolerates surrounding whitespace; anything else is
// rejected rather than guessed at, because the value picks both the data
// source and the bucket prefix written to.
func ParseGame(s string) (Game, error) {
	normalized := Game(strings.ToLower(strings.TrimSpace(s)))
	for _, g := range Games() {
		if g == normalized {
			return g, nil
		}
	}
	names := make([]string, 0, len(Games()))
	for _, g := range Games() {
		names = append(names, string(g))
	}
	return "", fmt.Errorf("source: unknown game %q, want one of %s", s, strings.Join(names, ", "))
}

// Want is the set of images a game wants mirrored, keyed by image key. The key
// is what state and the manifest record, and what the website asks for.
type Want map[string]mirror.Image

// Provider yields the want-list for one game.
//
// A provider owns three things the mirror deliberately knows nothing about:
// the card list, the source image URL for each card, and the object path each
// image is stored at. It is handed the bucket it is mirroring into, because a
// datastore-backed provider reads its card data from that same account's
// storage, and a filter of set codes to restrict the run to.
type Provider interface {
	// Game is the game this provider mirrors.
	Game() Game
	// BuildWant returns every image this game wants mirrored. setsFilter, when
	// non-nil, restricts the result to those set codes; providers whose data
	// has no set concept may ignore it, but should say so in their logs.
	BuildWant(ctx context.Context, setsFilter map[string]bool) (Want, error)
}

// SealedAware is implemented by providers whose want-list contains sealed
// product images, which the -skip-sealed flag acts on. A provider that does
// not implement it has no sealed pass, and that flag is refused rather than
// silently doing nothing.
type SealedAware interface {
	// IsSealedKey reports whether key names a sealed product image.
	IsSealedKey(key string) bool
}

// LogWant reports the shape of a want-list, so a run says what it is about to
// do before it spends hours doing it.
func LogWant(logger *log.Logger, game Game, want Want) {
	bySet := map[string]int{}
	for _, img := range want {
		bySet[img.SetCode]++
	}
	logger.Printf("game %s: %d images wanted across %d sets", game, len(want), len(bySet))
}

// SortedSetCodes returns the set codes present in want, sorted.
func SortedSetCodes(want Want) []string {
	seen := map[string]bool{}
	var out []string
	for _, img := range want {
		if img.SetCode == "" || seen[img.SetCode] {
			continue
		}
		seen[img.SetCode] = true
		out = append(out, img.SetCode)
	}
	sort.Strings(out)
	return out
}
