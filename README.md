# img-downloader
boring name for a complicated setup!

A standalone Go tool that mirrors MTG card and sealed product images (Scryfall
singles, TCGplayer sealed art) into a B2 bucket, bundling each set into a
deterministic zip and tracking fetch state so reruns only pull what changed.

## What it does

1. Downloads the Scryfall `default_cards` bulk-data file and streams it into
   an `id -> normal front image URL` map.
2. Downloads MTGJSON `AllPrintings.json.gz` and streams it into per-set
   `scryfallId` and sealed `tcgplayerProductId` lists.
3. Builds a want-list (`internal/mirror.BuildWant`) from the two sources,
   diffs it against the last saved state, and fetches everything missing or
   changed.
4. Writes each image to the bucket, updates `mirror-state.json`, and rebuilds
   the zip bundle plus manifest entry for any set whose contents changed.

## Bucket layout contract

This layout is settled with the project owner; do not change it without
updating both this tool and the website consumer.

- Singles object path: `singles/front/<c1>/<c2>/<scryfallId>.jpg`, where
  `c1`/`c2` are the first two characters of the id. Built from the id rather
  than from Scryfall'''s URL, so the layout is the mirror'''s own and pairs with
  `sealed/`; a key that is not a scryfall id is rejected rather than used to
  place an object. This happens to match where Scryfall files the same image,
  under `normal/` instead of `singles/`.
- Sealed object path: `sealed/<SETCODE>/<tcgplayerProductId>.jpg` (SETCODE is
  the uppercase MTGJSON code). Sealed sits under one shared prefix so the
  bucket root holds only the few top level trees rather than a directory per
  set code.
- Derived artifacts at the bucket base: `bundles/<SETCODE>-<hash>.zip`,
  `images-manifest.json`, `mirror-state.json`.
- Manifest JSON: `{"<SETCODE>": {"h": "<fnv64a hex>", "n": <imageCount>, "b": <totalBytes>}}`.
- Bundle zip: flat entries named `<imageKey>.jpg`, built deterministically
  (sorted, `zip.Store`, mtime epoch 0). Image key is the scryfallId for
  singles, `p-<SETCODE>-<tcgId>` for sealed.
- Bundle hash: fnv64a hex over sorted `"<key> <sha256hex>\n"` lines.
- State JSON: `{"<imageKey>": {"digest": "<sha256hex>", "fetchedAt": "RFC3339", "source": "<url>"}}`,
  plus an optional `"missing": true` on entries the source has no image for
  (see below); those carry no digest and are never bundled.
  A key is refetched when its stored `source` differs from the currently
  wanted URL. Sealed URLs never change, so sealed images are fetch-once.
  `source` keeps the whole Scryfall URL including its `?<epoch>` query, which
  Scryfall bumps whenever it reprocesses an image, so a reprocess is what
  triggers the refetch. The object path is derived from the URL path only, so
  the refetch overwrites in place rather than orphaning a second object, and
  the new sha256 changes the set's bundle hash so the zip rebuilds too. Note
  that this tracks reprocessing, not relocation: were Scryfall to change the
  path rather than the query, the new object is written correctly but the old
  one is left behind.
- No backfill marker file. Images are stored as jpg only, no webp, no cwebp.

## Usage

```
B2_BUCKET=<b2://bucket/prefix or local-dir> go run ./cmd/imgdl [-sets CSV] [-dry-run] [-skip-sealed]
```

Example local dev invocation:

```
B2_BUCKET=./tmp-mirror go run ./cmd/imgdl -sets NEO -dry-run
```

### Flags

- `-sets`: comma-separated set codes to mirror; empty means all sets.
- `-dry-run`: print the fetch plan without fetching or writing anything.
- `-skip-sealed`: skip the TCGplayer sealed product pass.
- `-refetch-sealed`: forget every sealed image already mirrored so this run
  stores them again. Needed only when the sealed object path changes: state
  keys on the source URL, a sealed URL never changes, so a path change is
  otherwise invisible to the diff and no run would write the new location.

### Environment variables

- `B2_BUCKET` (required): destination, either `b2://name/prefix` or a local
  directory path.
- `B2_ACCESS_KEY`, `B2_ACCESS_SECRET`: required when `B2_BUCKET` uses the
  `b2://` scheme. Not read from any config file, env only.

## GitHub Action

`.github/workflows/mirror.yml` runs the mirror on a daily cron (minute 17
past midnight UTC, chosen to avoid the top-of-hour scheduling drops GitHub
documents) and can also be triggered manually via workflow_dispatch with `sets`,
`dry_run` and `refetch_sealed` inputs. It needs two repo secrets:

- `B2_ACCESS_KEY`
- `B2_ACCESS_SECRET`

`B2_BUCKET` defaults to `b2://mtgban-images/magic` but is overridden by an
org or repo-level Actions variable of the same name if one is set; org-level
variables and secrets are picked up automatically, no workflow edits needed.

The job's `timeout-minutes` is 350, just under the 360 minute ceiling GitHub
enforces on hosted runners. Steady-state daily runs finish in minutes. The
initial backfill does not fit in it at all and is expected to take two or
three runs to converge; see below.

Notes on GitHub's scheduled workflows: schedules only fire from the default
branch, and on public repos GitHub disables schedules after 60 days with no
repository activity, so it needs a commit or manual run periodically to stay
alive. Scheduled runs are also best-effort and are commonly delayed under
load, which is what the minute-17 offset is hedging against.

## Initial backfill

The first run against an empty bucket has to fetch everything. As of the
2026-08-07 dry run that is **119,797 images**, nearly all of them singles on
the single host `cards.scryfall.io`; sealed images come from a second host
and are fetched in parallel, so the singles determine the wall clock.

The limiter books slots 100ms apart in absolute time rather than sleeping
100ms between requests, so a download shorter than the interval is absorbed
by it instead of adding to it. Locally that holds and the rate is one image
per 100ms — NEO measured 572 images in 57s, ARB 157 in 16s. **On a GitHub
runner it does not.** The 2026-08-07 backfill managed 62,111 images in its
350 minute budget, a measured **338ms per image**, 3.4x slower; downloads
there evidently exceed the interval, so the limiter stops absorbing them and
the rate becomes the transfer time.

At that rate a full backfill is about **11 hours**, comfortably past the 360
minute ceiling GitHub enforces. It therefore cannot complete in one scheduled
run, and is expected to take two or three, each resuming where the last was
killed. That works — the run above was SIGKILLed at its timeout and still
left all 62,111 images recorded — but if you want it done in one sitting, run
it locally, where ~100ms per image puts the whole thing near three hours.

Do not read a timed-out backfill as a failure. Check what a dry run reports as
still pending before assuming anything went wrong.

Run it locally or trigger the workflow manually (workflow_dispatch, leave
`sets` empty for a full run). State saves every 200 fetched images — about
every 20 seconds at the above rate — so an interrupted run resumes from
where it left off instead of restarting; rerun the same command and it only
fetches what is still missing.

The bundle rebuild is the slower half of a first run: each set is rebuilt by
reading its images back out of the bucket one at a time, so all ~119k reads
land there. The 2026-08-07 run managed fewer than 50 sets in 31 minutes, the
alphabetically-first sets being large ones, which puts the full pass in the
region of four hours on its own. The manifest is therefore snapshotted every
20 sets, on a context that outlives cancellation, and a cancelled rebuild
returns immediately instead of walking the remainder failing every read.
Without both, a run killed at its timeout lost every bundle it had built and
the phase could never converge across runs however many you ran.

Progress is reported every 30 seconds during the crawl, and every 20 bundles
during the rebuild:

```
fetched 24000/119797 (20%), 412 not published at source
rebuilt 240/1043 bundles
```

Both phases otherwise log only errors, which over a run this long makes a
healthy mirror indistinguishable from a hung one. The crawl reports on
elapsed time rather than an image count because the per-image rate differs
more than threefold between a local run and a CI runner, so a count tuned for
one is either silent or deafening on the other.

Two costs are specific to the first run. Every set's bundle is rebuilt
because the manifest starts empty, and a rebuild reads its members back out
of the bucket, so the run pulls all ~119k images down again (~30 GB of B2
egress, the billed direction) and uploads a comparable volume of zips. And
the state document reaches about 30 MB at full scale (~255 bytes per entry),
rewritten whole on every snapshot — roughly 9 GB of writes across a backfill.
That is B2 ingress, which is not billed, so it costs throughput rather than
money. Steady-state daily runs rebuild only the sets that changed and so pay
neither.

## Interrupts and durability

SIGINT (Ctrl-C) and SIGTERM stop the crawl gracefully: in-flight work is
abandoned, no bundle is rebuilt from a half-fetched set, and state is
flushed on a context that outlives the cancellation before the process exits
130. A second signal kills immediately, skipping that flush.

Losing that flush is cheap, because state is never ahead of the bucket. A
fetch records its state entry only after the object's `Close` returns, and
`Close` is what commits the upload to B2, so every failure path leaves the
key absent rather than falsely marked done. The invariant is that state is a
subset of what is actually stored, which makes both failure modes safe in
the same direction:

- The upload fails, no entry is written, and the next run refetches.
- The upload succeeds but the process dies before the next snapshot, so the
  image sits in the bucket unrecorded and the next run refetches it and
  rewrites identical bytes.

Neither can cause an image to be skipped, only refetched, so the ≤200 image
snapshot gap costs redundant work rather than correctness. Snapshots are
themselves single object commits, so a kill mid-write leaves the previous
snapshot intact rather than a truncated file.

One limit worth naming: the digest stored is computed from the bytes in
memory before the write, not read back afterwards, so it records what was
sent rather than a round-trip verification of what landed. A successful
`Close` is the integrity guard — the B2 upload is checksum-validated, so a
corrupted transfer surfaces as a `Close` error rather than a bad digest.

Under the workflow's `timeout-minutes`, a cancelled job gets only a short
grace period (the runner escalates SIGINT to SIGTERM to SIGKILL over roughly
ten seconds), which a ~30 MB state flush may not fit inside. The periodic
snapshot is the real safety net there, not the final flush.

### Aborting on a broken source

A source host that is down, blocking us, or has changed its URL shape would
otherwise fail every request for hours while the run dutifully worked through
its queue. The fetcher tracks each host's consecutive failures and aborts the
whole run once one reaches 50, exiting non-zero with which host tripped it.
State is still flushed, so the next run resumes rather than restarting.

Consecutive failures are counted rather than a total or a rate, because both
alternatives break on real data. Some sealed products were never published to
TCGplayer, so a healthy backfill produces a steady trickle of legitimate 404s
— measured at about 5.6% of sealed fetches, in bursts of up to ~13 in a row
where old sets sort next to each other. A total would eventually cross any
threshold on volume alone, and a rate accumulated over tens of thousands of
successful requests could never climb high enough to catch a host that broke
partway through. A streak resets on every success, so it stays quiet through
scattered misses and still fires within seconds whenever a host genuinely
stops answering, however deep into the run that happens.

An aborted run skips the bundle rebuild, on the same reasoning as an
interrupt. Ordinary scattered failures do not: those images stay out of the
bundle, and the bundle hash already accounts for their absence, so a handful
of permanently missing sealed images cannot block bundling forever.

### Images the source never published

A 404 or 410 means the host answered and has no image at that URL, which for
a given URL is permanent — unlike a timeout or a 5xx. Those are recorded in
state with `"missing": true` and no digest, so `NeedFetch` skips them on
later runs. Without that, every one would be re-requested on every run
forever: roughly 840 doomed requests a day, about 84 seconds of a run, and a
wall of alarming log lines each morning for images that are simply not
coming. They are logged as a count rather than a line each, are reported as
`notPublished` separately from `fetchFailed`, and do not fail the run.

The marker is keyed on the source URL like any other entry, so it is not
permanent in the wrong way: if the URL changes — a Scryfall reprocess bumping
its `?<epoch>` — the image is fetched again. Sealed URLs never change, so a
sealed product TCGplayer never published stays retired.

Markers written during a streak that goes on to trip the breaker are taken
back before the run ends. A host failing every request is broken, not
authoritative about what it publishes, and since a sealed URL never changes
there would be nothing to trigger a retry of anything it was wrongly asked
about during an outage.

## Moving the singles object path

Unlike sealed, this one is a plain prefix rename: `normal/...` becomes
`singles/...` with everything below it unchanged. That makes it a bucket-side
move rather than a refetch, which matters at ~116k images — re-pulling them
from Scryfall would take about eleven hours on a runner, for bytes already
sitting in the bucket.

State never records where an object was put, only its source URL and digest,
so a move leaves state correct: the next run's diff sees nothing to do, and
bundle rebuilds read the new location.

Do the move and the deploy close together. In between, a run would still fetch
nothing, but any set whose bundle needed rebuilding would look for images at
whichever path the running binary was built with.

## Moving the sealed object path

Changing `SealedObjectPath` alone does nothing. `NeedFetch` compares the
stored `source` against the wanted URL, and neither moves when only the
object path does, so a run reports success having written nothing to the new
location. `-refetch-sealed` is what applies it, by dropping sealed from state
so the diff re-queues them. Run it from the workflow with the
`refetch_sealed` dispatch input rather than by hand; at roughly 15k sealed
images and the runner's measured 338ms that is about 85 minutes, well inside
the job timeout.

Nothing deletes, so the copies at the old path remain until removed by hand.
The website reads this layout too, so it has to learn the new path before the
old tree goes away.

## Sealed images

Sealed product URLs come from TCGplayer and never change for a given
`tcgplayerProductId`, so once a sealed image is fetched it is never
refetched (fetch-once semantics). Singles are refetched only when their
Scryfall source URL changes.

## Known limitation: originalScryfallId overrides

mtgban's own `originalScryfallId` overrides (used to point a card at a
different card's artwork) are invisible to this tool, since it builds its
want-list purely from MTGJSON's `scryfallId` and Scryfall's bulk data. A
handful of overridden cards can end up missing from their set's bundle zip
as a result. Single-image serving for those cards is unaffected, since it
looks up the image directly by id rather than through the bundle.

## Future option: webp

Images are stored as jpg only today (no webp, no cwebp transcoding). Scryfall
now publishes native webp variants directly, including a `grid` size at
488x680, so if webp is wanted back later it can be pulled from Scryfall
without any local transcoding step.
