# img-downloader
boring name for a complicated setup!

A standalone Go tool that mirrors card and sealed product images into a B2
bucket, tracking fetch state so reruns only pull what changed.

It mirrors one game per run, chosen with `-game`. Magic is built from the
public MTGJSON and Scryfall bulk exports; every other game is built from
mtgban's own datastore, the same document the website loads.

## What it does

1. Asks the selected game's provider for a want-list: every image it should
   hold, as `key -> {source URL, object path, set code}`.
2. Diffs that against the last saved state and fetches everything missing or
   whose source URL changed.
3. Writes each image to the bucket and updates `mirror-state.json` and
   `images-manifest.json`.

### Providers

- **Magic** (`internal/source/magic`) downloads the Scryfall `default_cards`
  bulk file into an `id -> front image URL` map and MTGJSON
  `AllPrintings.json.gz` into per-set `scryfallId` and sealed
  `tcgplayerProductId` lists, then joins them.
- **Datastore** (`internal/source/datastore`) reads one game's datastore
  document with `mtgmatcher.Open` and takes each card's id and its `full`
  image URL straight from it. Lorcana and Riftbound are wired up. Nothing
  parses that document by hand, so this tool and the website cannot drift
  apart on its schema.

Adding a game means writing a `source.Provider` and listing it in
`source.Games()`; nothing in `internal/mirror` knows which game it is
mirroring.

## Bucket layout contract

This layout is settled with the project owner; do not change it without
updating both this tool and the website consumer.

Each game lives under its own bucket prefix — `b2://mtgban-images/magic`,
`.../lorcana`, `.../riftbound` — and everything below is relative to that
prefix. State and the manifest are single documents at the prefix, keyed by
image key with no record of which game a key came from, so two games sharing a
prefix would interleave their keys and each run would delete the other's
entries. `mirror-game.json` at the base records which game owns the prefix and
a run refuses to write a prefix another game claimed. A prefix with no marker
is unclaimed, not foreign, so the existing Magic bucket claims itself on its
next run.

### Magic

- Singles object path: `singles/grid/front/<c1>/<c2>/<scryfallId>.webp`, where
  `c1`/`c2` are the first two characters of the id. Built from the id rather
  than from Scryfall's URL, so the layout is the mirror's own and pairs with
  `sealed/`; a key that is not a scryfall id is rejected rather than used to
  place an object. This happens to match where Scryfall files the same image,
  under `normal/` instead of `singles/`.
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
  that moved, and converting the corpus to webp moved every object stored in
  its source's own format without changing one url. Entries written before
  `objectPath` was recorded are judged by their source's extension, which is a
  faithful account of what was stored back when bytes were written through
  untouched. Sealed URLs never change, so sealed images are fetch-once.
  `source` keeps the whole Scryfall URL including its `?<epoch>` query, which
  Scryfall bumps whenever it reprocesses an image, so a reprocess is what
  triggers the refetch. The object path is derived from the URL path only, so
  the refetch overwrites in place rather than orphaning a second object, and
  the new sha256 changes the set's bundle hash so the zip rebuilds too. Note
  that this tracks reprocessing, not relocation: were Scryfall to change the
  path rather than the query, the new object is written correctly but the old
  one is left behind.
- Singles are Scryfall's `grid` variant: their own webp encode at the same
  488x680 as the `normal` jpg, for a little over half the bytes (ARB measured
  20,967,673 -> 9,534,479, 54.5% smaller). Those are stored exactly as served,
  because they already are what the mirror would produce — see *Stored format*.
  The variant sits above the face in the path so a whole variant is one prefix,
  addable or droppable without moving anything else.

### Cleaning up

Superseded bundles are removed by the run that supersedes them. Nothing else
is: an image left behind by a layout or format change stays in the bucket,
because the mirror writes to the path the current layout asks for and has no
reason to look at the old one.

`scripts/prune-bucket.sh` finds those and, with `--apply`, removes them. It
reports and deletes nothing by default. Four things accumulate:

- `singles/front/**` — pre-`grid` jpg singles, superseded by
  `singles/grid/front/**`.
- `normal/**` — from before singles moved out of Scryfall's own url path.
- `sealed/**/*.jpg` — superseded when sealed began being converted to webp.
  These share a directory with their replacements, so this is the one case that
  can never be a prefix delete.
- `bundles/**` — generations from before the run started removing them.

Nothing is deleted unless its replacement is present, which is the whole safety
model: an orphan is only an orphan because something newer took its place, so
if that newer object is missing the old one is still the only copy. The script
holds those back and names them.

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

Quality is 80 and the corpus is stuck with it, since changing it re-digests and
re-stores every image that is not already webp. It is not higher because of
what the sources are: a jpg is already lossy and gains almost nothing, and at
q90 a jpg re-encode comes out larger than the jpg it came from.

A source the mirror cannot decode is a failed fetch, not a stored object.

### Datastore-backed games (Lorcana, Riftbound)

Same tree shape, different key namespace, because these games' ids are not
scryfall ids.

- Image key is the card's own mtgmatcher uuid for singles and `p-<uuid>` for
  sealed products. The key is the card's id, never the image URL's basename:
  Magic can use the basename because a Scryfall URL is named for the card,
  whereas these games' URLs are their CDN's own filenames.
- Singles object path: `singles/full/front/<c1>/<c2>/<uuid>.webp`. `full` is
  the mtgmatcher `Images` key mirrored, occupying the slot Magic's `grid`
  does; these games publish one image per card rather than a set of encodes.
- Sealed object path: `sealed/<c1>/<c2>/<uuid>.webp` — sharded on the id, with
  no per-set directory. Magic's sealed key encodes the set code because its id
  is a TCGplayer product id, meaningless on its own; a datastore game's product
  id is already a uuid in the same namespace as its cards, so pairing it with a
  set code buys nothing and costs the ability to parse the pair back. Lorcana
  set codes can be a single character and its product ids contain dashes (`1`
  and `1-600001`), so `p-1-1-600001` has no unambiguous split. Every key is
  therefore self-describing: a reader derives the object path from the key
  alone and needs no set code.
- `<c1>/<c2>` are the first two characters of the id, an id shorter than two
  characters being left-padded (`7` files under `0/7`) so every game has the
  same tree depth.
- The extension is always `webp`, whatever the CDN served: these games publish
  jpg and png, and the mirror converts on the way in. See *Stored format*.
- The set code is still recorded on each image and is what the manifest is
  keyed by; it is simply not in the object path.
- A printing's foil and nonfoil variants are separate uuids in mtgmatcher
  (`460` and `460_f`, `ogn-066-298_nonfoil` and `..._foil`) that share one
  image. The mirror stores the printing once under its base uuid, so a reader
  holding a finish uuid trims at the last underscore to find it.

## Usage

```
B2_BUCKET=<b2://bucket/prefix or local-dir> go run ./cmd/imgdl [-game NAME] [-sets CSV] [-dry-run] [-skip-sealed]
```

Example local dev invocations:

```
B2_BUCKET=./tmp-mirror go run ./cmd/imgdl -sets NEO -dry-run

B2_BUCKET=./tmp-lorcana IMGDL_DATASTORE=./lorcana.json.xz \
  go run ./cmd/imgdl -game lorcana -dry-run
```

### Flags

- `-game`: which game to mirror — `magic`, `lorcana` or `riftbound`. Defaults
  to `$IMGDL_GAME`, or `magic`. It selects both the data source and the key
  namespace, so an unknown value is refused rather than guessed at.
- `-sets`: comma-separated set codes to mirror; empty means all sets.
- `-dry-run`: print the fetch plan without fetching or writing anything.
- `-game`: which card game to mirror; also the bucket prefix written to.
- `-skip-sealed`: skip the sealed product pass.
- `-retry-missing`: forget the images a source answered it had none of, so
  this run asks again. A not-published marker keys on a URL that never
  changes, so the diff skips it forever; that is right for art nobody ever
  published and wrong the day the source finally publishes it.
- `-rebuild-bundles`: rebuild every set's bundle, disregarding the manifest.
  The manifest records what a bundle would contain, not that the object was
  written, so a manifest carried over from a build that stored no bundles
  matches perfectly and the ordinary diff finds no work to do.

Both sealed flags are refused for a game whose provider mirrors no sealed
images, rather than silently doing nothing.

### Environment variables

- `B2_BUCKET` (required): destination, either `b2://name/prefix` or a local
  directory path. It is the full base for this run, including the game
  segment; nothing is appended to it.
- `B2_ACCESS_KEY`, `B2_ACCESS_SECRET`: required when `B2_BUCKET` uses the
  `b2://` scheme. Not read from any config file, env only.
- `IMGDL_GAME`: default for `-game`.
- `IMGDL_DATASTORE`: required for every game except Magic. The datastore
  document to build the want-list from, either `b2://name/path/to/doc.json.xz`
  or a local file. This is the counterpart of the website's `datastore_path`
  config key, and it is a separate location from the image bucket: the
  datastore is the site's data, not the mirror's. `.xz` and `.gz` suffixes are
  decompressed by simplecloud on the way in. When it names a `b2://` bucket it
  reuses `B2_ACCESS_KEY`/`B2_ACCESS_SECRET`.

## GitHub Action

`.github/workflows/mirror.yml` runs the mirror on a daily cron (minute 17
past midnight UTC, chosen to avoid the top-of-hour scheduling drops GitHub
documents) and can also be triggered manually via workflow_dispatch with
`game`, `sets`, `dry_run`, `retry_missing`, `rebuild_bundles` and `datastore`
inputs. It needs two repo secrets:

- `B2_ACCESS_KEY`
- `B2_ACCESS_SECRET`

The cron fires with `game` unset, which means Magic — the scheduled run is
unchanged. Concurrency is grouped per game, since two games write different
prefixes and do not conflict, while two runs of one game would fight over its
state document.

Every game but Magic also needs `IMGDL_DATASTORE`, since it builds its
want-list from mtgban's own datastore rather than from public bulk data. That
is derived as `$IMGDL_DATASTORE_ROOT/<game>/allCards.json`, with
`IMGDL_DATASTORE_ROOT` defaulting to `b2://mtgban-datastore`, and the
`datastore` input overrides it outright for one run.

The datastore is a different bucket and takes a key of its own, since a B2
application key is scoped to a single bucket and the mirror only ever reads
the datastore while it writes images:

- `B2_DATASTORE_ACCESS_KEY`
- `B2_DATASTORE_ACCESS_SECRET`

Both are optional. Where they are unset the datastore falls back to
`B2_ACCESS_KEY`, for a deployment running one key across both buckets.

`B2_BUCKET` is derived as `$B2_BUCKET_ROOT/<game>`, with `B2_BUCKET_ROOT`
defaulting to `b2://mtgban-images`. An existing `B2_BUCKET` Actions variable
still wins outright, so a deployment that already sets one keeps exactly the
base it has today; org-level variables and secrets are picked up
automatically, no workflow edits needed. Pointing that override at one game's
prefix and then dispatching another game fails on the `mirror-game.json`
claim rather than corrupting either mirror.

Mirroring a game other than Magic also needs `IMGDL_DATASTORE` for that game,
which the workflow does not yet set — see the open cross-repo questions
below.

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
a given URL is permanent — unlike a timeout or a 5xx. So does a response
whose body is not an image: TCGplayer answers a missing product image with a
70 byte "Not Found" page under HTTP 200 and a `Content-Type` of `image/jpeg`,
so the status code says nothing and the body is the only honest part of it.
Both are recorded in state with `"missing": true` and no digest, so
`NeedFetch` skips them on later runs. Without that, every one would be re-requested on every run
forever: roughly 840 doomed requests a day, about 84 seconds of a run, and a
wall of alarming log lines each morning for images that are simply not
coming. They are logged as a count rather than a line each, are reported as
`notPublished` separately from `fetchFailed`, and do not fail the run.

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

## Future option: other variants

`image_uris` also carries `display` (672x936 webp, 63 KB for the same card),
which is 40% more resolution than what is stored today and still smaller than
the `normal` jpg. Every variant carries the same `?<epoch>`, so switching one
is a want-list change that the diff picks up on its own.

## Open cross-repo question: serving non-Magic images

The write side is only half of this. The website serves mirrored images from
`internal/offlineapi/images.go` (on its unmerged `offline-mode` branch), and
that handler cannot serve a non-Magic image today. Nothing here changes the
Magic path it already serves, but the datastore-backed games need a decision
on the read side before their images are reachable. Three things block them:

1. **Key validation.** `serveImage` gates `.webp` requests on
   `^[0-9a-f]{8}-...$` and `.jpg` requests on `^p-([0-9A-Z]{2,6})-([0-9]+)$`.
   A Lorcana uuid (`460`) or a Riftbound one (`ogn-066-298`) matches neither,
   so the request 404s before the bucket is touched.
2. **Extension as discriminator.** The handler treats `.webp` as "single" and
   `.jpg` as "sealed". That holds only because Magic's singles are always
   Scryfall webp and its sealed always TCGplayer jpg. These games serve
   whatever their CDN serves, so the two axes have to come apart: the `p-`
   prefix already determines singles-vs-sealed on its own, and the extension
   should just be the stored object's.
3. **`catalog.go`'s `imageKey`.** It derives the key as
   `path.Base(co.Images["full"])` minus `.jpg`. For Magic that happens to
   yield the scryfallId, because a Scryfall URL is named for the card. For
   these games it yields a CDN filename that names nothing — so the catalog
   would advertise a key the mirror never wrote, and the client would request
   an image that 404s forever. It validates nothing, so this fails silently.

**What this PR assumes, and why.** The mirror keys non-Magic images by the
card's own mtgmatcher uuid and derives every object path from the key alone
(see the layout contract above). That keeps one route, one bucket layout and
one path builder for all games, and confines the website's change to
validation: relax the single-key pattern per game, split the extension from
the singles/sealed decision, and switch `imageKey` to `co.UUID` for non-Magic
games. `images_path` is already per-deployment config, so each game's prefix
needs no new plumbing there.

The alternative considered was keying non-Magic singles by the image URL's
basename, which would leave `imageKey` untouched. It was rejected because the
key would then be a property of whatever URL the CDN currently serves rather
than of the card: a CDN reshuffle would re-key the entire mirror, and nothing
else in the system could name an image without first knowing its URL.

Two smaller things worth deciding at the same time:

- The handler indexes `key[0:1]` and `key[1:2]` after matching. Any relaxed
  pattern that admits a one-character key panics there unless it pads the
  same way `mirror.shard` does.
- Lorcana set codes can be a single character (`1`), so the manifest's set
  keys are not `^[0-9A-Z]{2,6}$` either. That is only a manifest concern here,
  since no non-Magic object path contains a set code.

None of this is settled — it is a cross-repo contract, and this PR implements
the mirror side of one proposal rather than declaring it decided.
