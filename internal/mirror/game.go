package mirror

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mtgban/simplecloud"
)

// gameMarkerName is the object recording which game owns a bucket prefix.
const gameMarkerName = "mirror-game.json"

// gameMarker is the mirror-game.json document.
type gameMarker struct {
	Game string `json:"game"`
}

// ErrGameMismatch is returned when a base already belongs to another game.
var ErrGameMismatch = fmt.Errorf("mirror: bucket prefix belongs to a different game")

// LoadGameMarker returns the game recorded at base, or "" if none is recorded.
func LoadGameMarker(ctx context.Context, bucket simplecloud.Reader, base string) (string, error) {
	reader, err := simplecloud.InitReader(ctx, bucket, JoinPath(base, gameMarkerName))
	if err != nil {
		if isNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer reader.Close()

	var marker gameMarker
	// B2 opens lazily, so a missing object surfaces here on first read.
	if err := json.NewDecoder(reader).Decode(&marker); err != nil {
		if isNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return marker.Game, nil
}

// SaveGameMarker records game as the owner of base.
func SaveGameMarker(ctx context.Context, bucket simplecloud.Writer, base, game string) error {
	return saveBucketJSON(ctx, bucket, base, gameMarkerName, gameMarker{Game: game})
}

// ClaimGame checks that base is this game's to write to, claiming it if it is
// unclaimed.
//
// State and the manifest are single documents at the base, keyed by image key
// with no record of which game a key came from. Pointing two games at one
// prefix would therefore not collide loudly; it would interleave their keys
// and, on the next run of either, delete the other's entries from a state
// document that no longer describes what is in the bucket. At Magic's ~120k
// images that is an eleven hour refetch, so it is worth one small object to
// make the mistake impossible rather than merely documented.
//
// An existing Magic prefix has no marker yet and is claimed on the next run,
// which is why absence is treated as unclaimed rather than as a mismatch.
// readOnly skips that write, for a dry run.
func ClaimGame(ctx context.Context, bucket simplecloud.ReadWriter, base, game string, readOnly bool) error {
	found, err := LoadGameMarker(ctx, bucket, base)
	if err != nil {
		return err
	}
	if found == game {
		return nil
	}
	if found != "" {
		return fmt.Errorf("%w: %s is mirroring %q, refusing to write %q there",
			ErrGameMismatch, base, found, game)
	}
	if readOnly {
		return nil
	}
	return SaveGameMarker(ctx, bucket, base, game)
}
