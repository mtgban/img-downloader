package mirror

import (
	"context"
	"log"

	"github.com/mtgban/simplecloud"
)

// Opts configures one mirror sync pass.
type Opts struct {
	Bucket simplecloud.ReadWriter
	Base   string
	Want   map[string]Image
	DryRun bool
	// SkipSealed excludes sealed keys from this run's fetch list, without
	// dropping them from Want; bundle membership is unaffected.
	SkipSealed bool
	Log        *log.Logger
}

// Result reports one pass's work.
type Result struct {
	Pending        int
	Fetched        int
	FetchFailed    int
	BundlesRebuilt int
}

// Run performs one incremental mirror pass; DryRun only prints the plan.
func Run(ctx context.Context, opts Opts) (Result, error) {
	var res Result
	logger := opts.Log
	if logger == nil {
		logger = log.Default()
	}

	state, err := LoadState(ctx, opts.Bucket, opts.Base)
	if err != nil {
		return res, err
	}
	manifest, err := LoadManifest(ctx, opts.Bucket, opts.Base)
	if err != nil {
		return res, err
	}

	fetches := NeedFetch(state, opts.Want)
	if opts.SkipSealed {
		fetches = dropSealed(fetches)
	}
	res.Pending = len(fetches)
	logger.Printf("%d images to fetch, %d wanted", len(fetches), len(opts.Want))
	if opts.DryRun {
		logger.Printf("dry run: no fetch or bundle work performed")
		return res, nil
	}

	fetched, failed, fetchErr := FetchAll(ctx, opts.Bucket, opts.Base, state, opts.Want, fetches, logger)
	res.Fetched, res.FetchFailed = fetched, failed

	codes := BundlesToRebuild(manifest, SetDigests(state, opts.Want))
	rebuilt, bundleErr := RebuildBundles(ctx, opts.Bucket, opts.Base, state, opts.Want, manifest, codes)
	res.BundlesRebuilt = rebuilt

	// manifest and state persist even when fetch or bundle work failed
	if err := SaveManifest(ctx, opts.Bucket, opts.Base, manifest); err != nil {
		return res, err
	}
	if err := SaveState(ctx, opts.Bucket, opts.Base, state); err != nil {
		return res, err
	}

	if fetchErr != nil {
		return res, fetchErr
	}
	return res, bundleErr
}

// dropSealed removes sealed keys from a NeedFetch result in place.
func dropSealed(keys []string) []string {
	out := keys[:0]
	for _, k := range keys {
		if !IsSealedKey(k) {
			out = append(out, k)
		}
	}
	return out
}
