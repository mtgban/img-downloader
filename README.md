# img-downloader
boring name for a complicated setup!

A standalone Go tool that mirrors card and sealed product images into a B2
bucket, tracking fetch state so reruns only pull what changed.

It mirrors one game per run, chosen with `-game`. Magic is built from the
public MTGJSON and Scryfall bulk exports; every other game is built from
mtgban's own datastore, the same document the website loads. Only Magic is
mirrored in production; see *Other games*.

## What it does

1. Asks the selected game's provider for a want-list: every image it should
   hold, as `key -> {source URL, object path, set code}`.
2. Diffs that against the last saved state and fetches everything missing,
   whose source URL changed, or that is stored somewhere other than its object
   path.
3. Writes each image to the bucket and updates `mirror-state.json` and
   `images-manifest.json`.

### Providers

- **Magic** (`internal/source/magic`) downloads the Scryfall `default_cards`
  bulk file into an `id -> front image URL` map and MTGJSON
  `AllPrintings.json.gz` into per-set `scryfallId` and sealed
  `tcgplayerProductId` lists, then joins them.
- **Datastore** (`internal/source/datastore`) reads one game's datastore
  document with `mtgmatcher.Open` and takes each card's product and its
  `full` image URL straight from it. Nothing parses that document by hand, so
  this tool and the website cannot drift apart on its schema.

`source.Games()` is whatever games the pinned go-mtgban registers a loader
for, so a datastore game added upstream needs only the dependency bumped. A
game with some other source needs a `source.Provider` of its own; nothing in
`internal/mirror` knows which game it is mirroring.

## Bucket layout contract

This layout is settled with the project owner; do not change it without
updating both this tool and the website consumer.

Each game lives under its own bucket prefix — `b2://mtgban-images/magic`,
`.../lorcana`, `.../pokemon` and so on — and everything below is relative to
that prefix. State and the manifest are single documents at the prefix, keyed by
image key with no record of which game a key came from, so two games sharing a
prefix would interleave their keys and each run would delete the other's
entries. `mirror-game.json` at the base records which game owns the prefix and
a run refuses to write a prefix another game claimed. A prefix with no marker
is unclaimed, and the first run against it claims it.

### Magic

- Singles object path: `singles/grid/front/<c1>/<c2>/<scryfallId>.webp`, where
  `c1`/`c2` are the first two characters of the id. Built from the id rather
  than from Scryfall's URL, so the layout is the mirror's own and pairs with
  `sealed/`; a key that is not a scryfall id is rejected rather than used to
  place an object.
- Sealed object path: `sealed/<SETCODE>/<tcgplayerProductId>.webp` (SETCODE is
  the uppercase MTGJSON code). Sealed sits under one shared prefix so the
  bucket root holds only the few top level trees rather than a directory per
  set code. TCGplayer serves jpg; the mirror converts it on the way in.
- Derived artifacts at the bucket base: `bundles/<SETCODE>-<hash>.zip`,
  `images-manifest.json`, `mirror-state.json`. A rebuild deletes the bundle it
  supersedes, since the manifest entry it replaces is the only record that
  object's hash ever had. Bundles are uncompressed, so a generation nothing
  points at costs about what the sets it covers cost.
- Manifest JSON: `{"<SETCODE>": {"h": "<fnv64a hex>", "n": <imageCount>, "b": <totalBytes>}}`.
- Bundle zip: flat entries named `<imageKey>.webp`, built deterministically
  (sorted, `zip.Store`, mtime epoch 0). Image key is the scryfallId for
  singles, `p-<SETCODE>-<tcgId>` for sealed.
- Bundle hash: fnv64a hex over sorted `"<key> <sha256hex>\n"` lines.
- State JSON: `{"<imageKey>": {"digest": "<sha256hex>", "fetchedAt": "RFC3339",
  "source": "<url>", "objectPath": "<path>"}}`, plus an optional
  `"missing": true` on entries the source has no image for (see below); those
  carry no digest and are never bundled. `digest` is of the bytes stored, which
  are not the bytes served — see *Stored format*.
  A key is refetched when its stored `source` differs from the currently wanted
  URL, or when its `objectPath` does: a source url alone cannot see an object
  that a format or layout change moved. Sealed URLs never change, so sealed
  images are fetch-once.
  `source` keeps the whole Scryfall URL including its `?<epoch>` query, which
  Scryfall bumps whenever it reprocesses an image, so a reprocess is what
  triggers the refetch. The object path is built from the id, not the URL, so
  the refetch overwrites in place rather than orphaning a second object, and
  the new sha256 changes the set's bundle hash so the zip rebuilds too.
- Singles are Scryfall's `grid` variant: their own webp encode at the same
  488x680 as the `normal` jpg, for a little over half the bytes (ARB measured
  20,967,673 -> 9,534,479, 54.5% smaller). Those are stored exactly as served,
  because they already are what the mirror would produce — see *Stored format*.
  The variant sits above the face in the path so a whole variant is one prefix,
  addable or droppable without moving anything else.

### Cleaning up

Superseded bundles are removed by the run that supersedes them. A removal that
fails is only logged, so a bundle the manifest no longer names can remain.

`scripts/prune-bucket.sh` finds those and, with `--apply`, removes them. It
reports and deletes nothing by default, and skips everything when it cannot
read a usable `images-manifest.json`, since that is the only record of which
bundles are current. Do not run it during a mirror run: a bundle that run has
just built is not in the manifest until its next snapshot.

### Stored format

Every mirrored object is webp, whatever its source served, so that nothing
downstream has to ask what an image is: object paths, bundle entry names and
the website's cache urls all end the same way, and the client has one content
type rather than a guess to make.

Sources arrive as webp (Scryfall), jpg (TCGplayer, Lorcana) and png
(Riftbound). Conversion happens on fetch, before the digest is taken, because
the digest has to describe what is in the bucket rather than what the source
served — it is what bundle hashes are built from.

Two rules do most of the work:

- **Bytes already webp are passed through untouched.** Re-encoding lossy bytes
  into the same lossy format spends quality to save nothing. Measured on one
  Scryfall grid image, re-encoding it at q80 produced 78,356 bytes against the
  73,756 it arrived as: 6% *larger*, and worse looking. Since Scryfall is
  nearly the whole corpus, this is also what keeps the conversion cheap — those
  objects neither move nor get rewritten.
- **Anything larger than 488x680 is scaled down to fit,** preserving aspect
  ratio and never scaling up. That is Scryfall's grid geometry, which the Magic
  corpus already holds, and it clears the largest the website ever draws a card
  (its lightbox caps at 440 css px; everything else is a thumbnail). A png card
  scan measured 1,476,439 bytes at 744x1040 and 77,154 at 486x680: 19x smaller.

Quality is 80. Changing it, or the encoder, applies only to images fetched
afterwards: the diff goes by source URL and object path, not by how an object
was encoded, so stored objects keep their encode until their source changes.
It is not higher because of what the sources are: a jpg is already lossy and gains almost nothing, and at
q90 a jpg re-encode comes out larger than the jpg it came from.

A source the mirror cannot decode is a failed fetch, not a stored object.

### Datastore-backed games (everything except Magic)

Not mirrored in production yet; see *Other games*. Same tree shape, different
key namespace, because these games' ids are not scryfall ids.

- Image key for a single is its TCGplayer product id (`tcgplayerProductId`),
  or for a card that names no product, the key its finishes share
  (`mtgmatcher.PrintingKey`); for a sealed product it is `p-<uuid>`. Keys are
  read from the card's fields, never from the image URL or the uuid's shape:
  these games' URLs are their CDN's own filenames, and a datastore uuid is its
  builder's to spell and respell.
- Singles object path: `singles/full/front/<c1>/<c2>/<key>.webp`. `full` is
  the mtgmatcher `Images` key mirrored, occupying the slot Magic's `grid`
  does; these games publish one image per card rather than a set of encodes.
- Sealed object path: `sealed/<SETCODE>/<uuid>.webp`, the same shape Magic's
  sealed takes, so one layout describes the bucket whatever game wrote it.
  The *key* carries no set code: Lorcana set codes can be a single character
  and its product ids contain dashes (`1` and `1-600001`), so `p-1-1-600001`
  would have no unambiguous split. Nothing turns a key back into a path, since
  every path a bundle reads comes from the want-list.
- `<c1>/<c2>` are the first two characters of the key, a key shorter than two
  characters being left-padded (`7` files under `0/7`) so every game has the
  same tree depth.
- The extension is always `webp`, whatever the CDN served: these games publish
  jpg and png, and the mirror converts on the way in. See *Stored format*.
- The set code is recorded on each image and is what the manifest is keyed
  by; it is in the sealed object path but not the singles one.
- Every finish of a product is an entry of its own in mtgmatcher
  (`dtd011_502592` and `dtd011_502592_rainbowfoil`), and they share one image.
  The mirror walks those entries the way the website's catalog does and files
  the image once under the key they share. The website computes the same key
  from the same fields (`internal/offlineapi`'s `datastoreImageKey`), so the
  two change together.

## Usage

```
B2_BUCKET=<b2://bucket/prefix or local-dir> go run ./cmd/imgdl [flags]
```

Example local dev invocations:

```
B2_BUCKET=./tmp-mirror go run ./cmd/imgdl -sets NEO -dry-run

B2_BUCKET=./tmp-lorcana IMGDL_DATASTORE=./lorcana.json.xz \
  go run ./cmd/imgdl -game lorcana -dry-run
```

### Flags

- `-game`: which game to mirror. The list is read from what mtgmatcher
  registers rather than written out, so it is whatever the pinned go-mtgban
  provides — run with an unknown game to have it name them. Defaults to
  `$IMGDL_GAME`, or `magic`. It selects both the data source and the key
  namespace, so an unknown value is refused rather than guessed at.
- `-sets`: comma-separated set codes to mirror; empty means all sets.
- `-dry-run`: print the fetch plan without fetching or writing anything.
- `-skip-sealed`: skip the sealed product pass.
- `-retry-missing`: forget the images a source answered it had none of, so
  this run asks again. A not-published marker keys on a URL that never
  changes, so the diff skips it forever; that is right for art nobody ever
  published and wrong the day the source finally publishes it.
- `-rebuild-bundles`: rebuild every set's bundle, disregarding the manifest.
  The manifest records what a bundle would contain, not that the object was
  written, so a manifest carried over from a build that stored no bundles
  matches perfectly and the ordinary diff finds no work to do.

### Environment variables

- `B2_BUCKET` (required): destination, either `b2://name/prefix` or a local
  directory path. It is the full base for this run, including the game
  segment; nothing is appended to it.
- `B2_IMAGES_KEY`, `B2_IMAGES_SECRET`: required when `B2_BUCKET` uses the
  `b2://` scheme. Not read from any config file, env only.
- `IMGDL_GAME`: default for `-game`.
- `IMGDL_DATASTORE`: required for every game except Magic. The datastore
  document to build the want-list from, either `b2://name/path/to/doc.json.xz`
  or a local file. This is the counterpart of the website's `datastore_path`
  config key, and it is a separate location from the image bucket: the
  datastore is the site's data, not the mirror's. `.xz` and `.gz` suffixes are
  decompressed by simplecloud on the way in.
- `B2_DATASTORE_KEY`, `B2_DATASTORE_SECRET`: the key a `b2://`
  `IMGDL_DATASTORE` is read with. Where they are unset it falls back to
  `B2_IMAGES_KEY`/`B2_IMAGES_SECRET`.

## GitHub Action

`.github/workflows/mirror.yml` runs the mirror on a daily cron (minute 17
past midnight UTC, chosen to avoid the top-of-hour scheduling drops GitHub
documents) and can also be triggered manually via workflow_dispatch with
`game`, `sets`, `dry_run`, `retry_missing`, `rebuild_bundles` and `datastore`
inputs. It needs two secrets, which it passes to imgdl as `B2_IMAGES_KEY` and
`B2_IMAGES_SECRET`:

- `B2_APPLICATION_KEY_ID_IMAGES`
- `B2_APPLICATION_KEY_IMAGES`

The cron fires with `game` unset, which means Magic, the only game mirrored
in production. Concurrency is grouped per game, since two games write different
prefixes and do not conflict, while two runs of one game would fight over its
state document.

Every game but Magic also needs `IMGDL_DATASTORE`, since it builds its
want-list from mtgban's own datastore rather than from public bulk data. That
is derived as `$IMGDL_DATASTORE_ROOT/<game>/<game>.json.xz`, with
`IMGDL_DATASTORE_ROOT` defaulting to `b2://mtgban-datastore`, and the
`datastore` input overrides it outright for one run.

The datastore is a different bucket and takes a key of its own, since a B2
application key is scoped to a single bucket and the mirror only ever reads
the datastore while it writes images. The workflow passes it as
`B2_DATASTORE_KEY` and `B2_DATASTORE_SECRET`:

- `B2_APPLICATION_KEY_ID_DATASTORE`
- `B2_APPLICATION_KEY_DATASTORE`

Both are optional. Where they are unset the datastore falls back to the images
key, for a deployment running one key across both buckets.

`B2_BUCKET` is derived as `$B2_BUCKET_ROOT/<game>`, with `B2_BUCKET_ROOT`
defaulting to `b2://mtgban-images`. A `B2_BUCKET` Actions variable wins
outright; org-level variables and secrets are picked up
automatically, no workflow edits needed. Pointing that override at one game's
prefix and then dispatching another game fails on the `mirror-game.json`
claim rather than corrupting either mirror.

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

The first run against an empty bucket has to fetch everything: **121,640
images** as of the 2026-10-01 run, nearly all of them singles on the single
host `cards.scryfall.io`; sealed images come from a second host
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
reading its images back out of the bucket one at a time, so all ~122k reads
land there. The 2026-08-07 run managed fewer than 50 sets in 31 minutes, the
alphabetically-first sets being large ones, which puts the full pass in the
region of four hours on its own. The manifest is therefore snapshotted every
20 sets, on a context that outlives cancellation, and a cancelled rebuild
returns immediately instead of walking the remainder failing every read.
Without both, a run killed at its timeout would lose every bundle it had
built, and the phase could never converge across runs.

Progress is reported every 30 seconds in both phases, and the rebuild also
reports each manifest snapshot, every 20 sets:

```
fetched 24000/121640 (19%), 120 not published at source
rebuilt 240/868 bundles
```

Both phases otherwise log only errors, which over a run this long makes a
healthy mirror indistinguishable from a hung one. The crawl reports on
elapsed time rather than an image count because the per-image rate differs
more than threefold between a local run and a CI runner, so a count tuned for
one is either silent or deafening on the other.

Two costs are specific to the first run. Every set's bundle is rebuilt
because the manifest starts empty, and a rebuild reads its members back out
of the bucket, so the run pulls all ~122k images down again (~30 GB of B2
egress, the billed direction) and uploads a comparable volume of zips. And
the state document reaches about 40 MB at full scale (~120k entries of ~340
bytes), rewritten whole on every snapshot — roughly 12 GB of writes across a
backfill. That is B2 ingress, which is not billed, so it costs throughput
rather than money. Steady-state daily runs rebuild only the sets that changed
and save state once, so they pay neither.

## Interrupts and durability

SIGINT (Ctrl-C) and SIGTERM stop the crawl gracefully: in-flight work is
abandoned, no bundle is rebuilt from a half-fetched set, and state is
flushed on a context that outlives the cancellation before the process exits
130. A second signal kills immediately, skipping that flush.

Losing that flush is cheap, because state is never ahead of the bucket. A
fetch records its state entry only after the object's `Close` returns, and
`Close` is what commits the upload to B2; a write that fails is aborted
instead, so no partial object is published. Every failure path leaves the key
absent rather than falsely marked done. The invariant is that state is a
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
ten seconds), which a ~40 MB state flush may not fit inside. The periodic
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
a given URL is permanent — unlike a timeout or a 5xx. So does a 403: a bucket
fronted without public ListBucket answers a key that is not there with
AccessDenied rather than Not Found, which is how TCGplayer's CDN reports a
product it holds no artwork for. A 403 that really is the host refusing us
outright would be every request rather than one in seven, and that trips the
consecutive-failure breaker, which takes back every marker the streak wrote.

So does a response whose body is not an image: TCGplayer answers a missing
product image with a 70 byte "Not Found" page under HTTP 200 and a
`Content-Type` of `image/jpeg`, so the status code says nothing and the body
is the only honest part of it.

Both are recorded in state with `"missing": true` and no digest, so
`NeedFetch` skips them on later runs; without that, every one would be
re-requested on every run forever, with a failure logged for each. They are
logged as a count rather than a line each, are reported as `notPublished`
separately from `fetchFailed`, and do not fail the run.

The marker is keyed on the source URL like any other entry, so it is not
permanent in the wrong way: if the URL changes — a Scryfall reprocess bumping
its `?<epoch>` — the image is fetched again. Sealed URLs never change, so a
sealed product TCGplayer never published stays retired until `-retry-missing`
drops the marker and the diff asks again, which is what to run if a source
publishes art it previously lacked.

Markers written during a streak that goes on to trip the breaker are taken
back before the run ends. A host failing every request is broken, not
authoritative about what it publishes, and since a sealed URL never changes
there would be nothing to trigger a retry of anything it was wrongly asked
about during an outage.

## Known limitation: originalScryfallId overrides

mtgban's own `originalScryfallId` overrides (used to point a card at a
different card's artwork) are invisible to this tool, since it builds its
want-list purely from MTGJSON's `scryfallId` and Scryfall's bulk data. A
handful of overridden cards can end up missing from their set's bundle zip
as a result. Single-image serving for those cards is unaffected, since it
looks up the image directly by id rather than through the bundle.

## Future option: other variants

`image_uris` also carries `display` (672x936 webp, 63 KB for the same card),
which is 40% more resolution than what is stored today and still smaller than
the `normal` jpg. Every variant carries the same `?<epoch>`, so switching one
is a want-list change that the diff picks up on its own.

## Other games

Only Magic is mirrored in production. The datastore games run end to end
through `-game` and the workflow's `game` input, but the website cannot serve
their images yet, so the scheduled run mirrors Magic alone.

The key a datastore single is filed under (`singleKey` in
`internal/source/datastore`) is a contract with the website's offline
catalog, which computes the same expression in `internal/offlineapi`'s
`datastoreImageKey`. Whatever the website settles on for serving these games
has to keep the two in step.
