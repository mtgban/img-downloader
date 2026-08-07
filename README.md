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

- Singles object path: the Scryfall URL path with the leading slash
  stripped, e.g. `normal/front/<c1>/<c2>/<scryfallId>.jpg`, where `c1`/`c2`
  are the first two characters of the id.
- Sealed object path: `<SETCODE>/sealed/<tcgplayerProductId>.jpg` (SETCODE is
  the uppercase MTGJSON code).
- Derived artifacts at the bucket base: `bundles/<SETCODE>-<hash>.zip`,
  `images-manifest.json`, `mirror-state.json`.
- Manifest JSON: `{"<SETCODE>": {"h": "<fnv64a hex>", "n": <imageCount>, "b": <totalBytes>}}`.
- Bundle zip: flat entries named `<imageKey>.jpg`, built deterministically
  (sorted, `zip.Store`, mtime epoch 0). Image key is the scryfallId for
  singles, `p-<SETCODE>-<tcgId>` for sealed.
- Bundle hash: fnv64a hex over sorted `"<key> <sha256hex>\n"` lines.
- State JSON: `{"<imageKey>": {"digest": "<sha256hex>", "fetchedAt": "RFC3339", "source": "<url>"}}`.
  A key is refetched when its stored `source` differs from the currently
  wanted URL. Sealed URLs never change, so sealed images are fetch-once.
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

### Environment variables

- `B2_BUCKET` (required): destination, either `b2://name/prefix` or a local
  directory path.
- `B2_ACCESS_KEY`, `B2_ACCESS_SECRET`: required when `B2_BUCKET` uses the
  `b2://` scheme. Not read from any config file, env only.

## GitHub Action

`.github/workflows/mirror.yml` runs the mirror on a daily cron (minute 17
past midnight UTC, chosen to avoid the top-of-hour scheduling drops GitHub
documents) and can also be triggered manually via workflow_dispatch with
`sets` and `dry_run` inputs. It needs two repo secrets:

- `B2_ACCESS_KEY`
- `B2_ACCESS_SECRET`

`B2_BUCKET` defaults to `b2://mtgban-images/magic` but is overridden by an
org or repo-level Actions variable of the same name if one is set; org-level
variables and secrets are picked up automatically, no workflow edits needed.

Notes on GitHub's scheduled workflows: schedules only fire from the default
branch, and on public repos GitHub disables schedules after 60 days with no
repository activity, so it needs a commit or manual run periodically to stay
alive.

## Initial backfill

The first run against an empty bucket has to fetch everything: roughly 90k
images at the enforced 100ms per host delay, about 3-4 hours total. Run it
locally or trigger the workflow manually (workflow_dispatch, leave `sets`
empty for a full run). State saves every 200 fetched images, so an
interrupted run resumes from where it left off instead of restarting; rerun
the same command and it only fetches what is still missing.

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
