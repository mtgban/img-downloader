package mirror

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mtgban/simplecloud"
)

func TestClaimGameClaimsAnUnclaimedBase(t *testing.T) {
	ctx := context.Background()
	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}

	if err := ClaimGame(ctx, bucket, base, "magic", false); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	got, err := LoadGameMarker(ctx, bucket, base)
	if err != nil {
		t.Fatal(err)
	}
	if got != "magic" {
		t.Errorf("marker = %q, want %q", got, "magic")
	}
	// the same game claiming again is the ordinary daily run
	if err := ClaimGame(ctx, bucket, base, "magic", false); err != nil {
		t.Errorf("reclaim by same game: %v", err)
	}
}

// The Magic bucket predates the marker, so its next run must claim it rather
// than refuse to touch it; an unmarked base is unclaimed, not foreign.
func TestClaimGameTreatsMissingMarkerAsUnclaimed(t *testing.T) {
	ctx := context.Background()
	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}

	got, err := LoadGameMarker(ctx, bucket, base)
	if err != nil {
		t.Fatalf("LoadGameMarker on empty base: %v", err)
	}
	if got != "" {
		t.Errorf("marker on empty base = %q, want empty", got)
	}
}

func TestClaimGameRefusesAnotherGamesBase(t *testing.T) {
	ctx := context.Background()
	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}

	if err := ClaimGame(ctx, bucket, base, "magic", false); err != nil {
		t.Fatal(err)
	}
	err := ClaimGame(ctx, bucket, base, "lorcana", false)
	if err == nil {
		t.Fatal("ClaimGame(lorcana) on a magic base = nil, want a mismatch error")
	}
	if !errors.Is(err, ErrGameMismatch) {
		t.Errorf("error = %v, want ErrGameMismatch", err)
	}
	// the marker must not have been overwritten by the refused claim
	got, err := LoadGameMarker(ctx, bucket, base)
	if err != nil {
		t.Fatal(err)
	}
	if got != "magic" {
		t.Errorf("marker after refused claim = %q, want %q", got, "magic")
	}
}

func TestClaimGameDryRunDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}

	if err := ClaimGame(ctx, bucket, base, "magic", true); err != nil {
		t.Fatalf("dry run claim: %v", err)
	}
	got, err := LoadGameMarker(ctx, bucket, base)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("dry run wrote marker %q, want none", got)
	}
}

// A dry run against someone else's prefix should still say so, since telling
// the operator is the whole point of running one.
func TestClaimGameDryRunStillReportsMismatch(t *testing.T) {
	ctx := context.Background()
	base := filepath.ToSlash(t.TempDir())
	bucket := &simplecloud.FileBucket{}

	if err := ClaimGame(ctx, bucket, base, "magic", false); err != nil {
		t.Fatal(err)
	}
	if err := ClaimGame(ctx, bucket, base, "riftbound", true); !errors.Is(err, ErrGameMismatch) {
		t.Errorf("dry run against a foreign base = %v, want ErrGameMismatch", err)
	}
}
