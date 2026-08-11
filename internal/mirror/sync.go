package mirror

import (
	"context"
	"errors"
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
	// RebuildBundles rebuilds every set's bundle regardless of what the
	// manifest says is current. The manifest records what a bundle would
	// contain, not that the object exists, so a manifest written while nothing
	// was building bundles describes bundles that were never stored; the diff
	// then finds nothing to do and the run is a silent no-op.
	RebuildBundles bool
	// RetryMissing forgets the images a source answered it had none of, so
	// this run asks again. A not-published marker keys on a URL that never
	// changes, so the diff skips it forever; that is right for art nobody ever
	// published and wrong the day the source finally publishes it.
	RetryMissing bool
	// IsSealedKey reports whether a key names a sealed product image, for
	// SkipSealed. It comes from the provider, because what counts as sealed is
	// the game's business rather than the mirror's; nil means the Magic
	// p-<SETCODE>-<tcgId> shape, which is what every existing state document
	// uses.
	IsSealedKey func(key string) bool
	Log         *log.Logger
}

// sealedPredicate returns the configured sealed-key test, defaulting to the
// Magic key shape.
func (o Opts) sealedPredicate() func(string) bool {
	if o.IsSealedKey != nil {
		return o.IsSealedKey
	}
	return IsSealedKey
}

// Result reports one pass's work.
type Result struct {
	Pending        int
	Fetched        int
	FetchFailed    int
	NotPublished   int
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

	isSealed := opts.sealedPredicate()
	if opts.RetryMissing {
		logger.Printf("re-asking for %d images previously not published at source", forgetMissing(state))
	}

	fetches := NeedFetch(state, opts.Want)
	if opts.SkipSealed {
		fetches = dropSealed(fetches, isSealed)
	}
	res.Pending = len(fetches)
	logger.Printf("%d images to fetch, %d wanted", len(fetches), len(opts.Want))
	if opts.DryRun {
		logger.Printf("dry run: no fetch or bundle work performed")
		return res, nil
	}

	fetched, failed, fetchErr := FetchAll(ctx, opts.Bucket, opts.Base, state, opts.Want, fetches, logger)
	res.Fetched, res.FetchFailed = fetched, failed
	res.NotPublished = NotPublishedCount(state, opts.Want)

	// a run that stopped early skips bundle work: rebuilding from a half-fetched
	// set would only produce a bundle the next run has to redo anyway. Ordinary
	// scattered failures do not qualify, since those images stay absent from
	// state and the bundle hash already accounts for their absence.
	var bundleErr error
	if ctx.Err() != nil || errors.Is(fetchErr, ErrTooManyFailures) {
		logger.Printf("run stopped early, skipping bundle rebuild")
	} else {
		digests := SetDigests(state, opts.Want)
		var codes []string
		if opts.RebuildBundles {
			codes = AllSetCodes(digests)
			logger.Printf("rebuilding every bundle: %d sets", len(codes))
		} else {
			codes = BundlesToRebuild(manifest, digests)
		}
		res.BundlesRebuilt, bundleErr = RebuildBundles(ctx, opts.Bucket, opts.Base, state, opts.Want, manifest, codes, logger)
	}

	// manifest and state persist even when the run was interrupted or work failed
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	if err := SaveManifest(saveCtx, opts.Bucket, opts.Base, manifest); err != nil {
		return res, err
	}
	if err := SaveState(saveCtx, opts.Bucket, opts.Base, state); err != nil {
		return res, err
	}

	if fetchErr != nil {
		return res, fetchErr
	}
	return res, bundleErr
}

// forgetMissing drops every not-published marker from state, returning how
// many, so the diff queues those images again.
func forgetMissing(state State) int {
	n := 0
	for key, entry := range state {
		if entry.Missing {
			delete(state, key)
			n++
		}
	}
	return n
}

// dropSealed removes sealed keys from a NeedFetch result in place.
func dropSealed(keys []string, isSealed func(string) bool) []string {
	out := keys[:0]
	for _, k := range keys {
		if !isSealed(k) {
			out = append(out, k)
		}
	}
	return out
}
