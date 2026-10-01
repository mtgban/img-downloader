#!/usr/bin/env bash
#
# Delete the bundles images-manifest.json no longer names.
#
# A rebuild removes the bundle it supersedes, but a removal that fails is only
# logged, so a generation nothing points at can remain. This finds those and,
# with --apply, removes them. Bundles are uncompressed, so each one left behind
# costs about what its set does.
#
# The manifest is the only record of which bundles are current, so nothing is
# deleted when it cannot be read. Do not run this while a mirror run is in
# progress: a bundle that run has just built is not in the manifest until its
# next snapshot.
#
# Usage:
#   scripts/prune-bucket.sh                 # report only, deletes nothing
#   scripts/prune-bucket.sh --apply         # actually delete
#   BASE=b2://mtgban-images/magic scripts/prune-bucket.sh
#
set -euo pipefail

BASE=${BASE:-b2://mtgban-images/magic}
JOBS=${JOBS:-16}
APPLY=0
KEEP=0

while [ $# -gt 0 ]; do
    case "$1" in
        --apply) APPLY=1 ;;
        --keep-lists) KEEP=1 ;;
        --base) shift; BASE=$1 ;;
        -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
    shift
done

command -v b2 >/dev/null || { echo "b2 CLI not found" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq not found (needed to read the manifest)" >&2; exit 1; }

case "$BASE" in
    b2://*) ;;
    *) echo "BASE must be a b2:// uri, got: $BASE" >&2; exit 2 ;;
esac
BASE=${BASE%/}
BUCKET=${BASE#b2://}; BUCKET=${BUCKET%%/*}
PREFIX=${BASE#b2://"$BUCKET"/}
[ "$PREFIX" = "$BASE" ] && PREFIX=""

WORK=$(mktemp -d)
cleanup() { [ "$KEEP" = 1 ] && echo "lists kept in $WORK" || rm -rf "$WORK"; }
trap cleanup EXIT

# b2 prints object names bucket-relative in some versions and prefix-relative
# in others; normalise to paths under BASE so the rest of the script can do
# plain set arithmetic on them.
rel() {
    sed -e "s|^b2://[^/]*/||" ${PREFIX:+-e "s|^${PREFIX}/||"} -e '/^$/d'
}

# list <subpath-or-pattern>  -> object paths relative to BASE, sorted
list() {
    b2 ls --recursive --with-wildcard "$BASE/$1" 2>/dev/null | rel | LC_ALL=C sort
}

say() { printf '%s\n' "$*"; }
rule() { printf '%s\n' "----------------------------------------------------------------"; }

say "base:   $BASE"
say "bucket: $BUCKET"
say "prefix: ${PREFIX:-<root>}"
say "mode:   $([ "$APPLY" = 1 ] && echo 'APPLY - objects will be deleted' || echo 'report only')"
rule

# ------------------------------------------------------------ bundles --------
# A bundle is current only if images-manifest.json names its hash. Anything
# else under bundles/ is a generation nothing points at any more, and since
# bundles use zip.Store each generation is about the size of the corpus.
say "scanning bundles/ against images-manifest.json..."
if ! b2 file cat "$BASE/images-manifest.json" 2>/dev/null > "$WORK/manifest.json"; then
    b2 cat "$BASE/images-manifest.json" > "$WORK/manifest.json" 2>/dev/null || true
fi
if [ ! -s "$WORK/manifest.json" ] || ! jq -e 'type == "object" and length > 0' "$WORK/manifest.json" >/dev/null 2>&1; then
    say "  could not read a usable images-manifest.json - skipping bundles"
    : > "$WORK/bundles.delete.txt"
else
    jq -r 'to_entries[] | "bundles/\(.key)-\(.value.h).zip"' "$WORK/manifest.json" \
        | LC_ALL=C sort > "$WORK/live-bundles.txt"
    list 'bundles/*.zip' > "$WORK/present-bundles.txt"
    LC_ALL=C comm -13 "$WORK/live-bundles.txt" "$WORK/present-bundles.txt" > "$WORK/bundles.delete.txt"
    say "bundles/ - superseded generations"
    say "  current:   $(wc -l < "$WORK/live-bundles.txt" | tr -d ' ')"
    say "  present:   $(wc -l < "$WORK/present-bundles.txt" | tr -d ' ')"
    say "  to delete: $(wc -l < "$WORK/bundles.delete.txt" | tr -d ' ')"
fi
rule

# ------------------------------------------------------------------ totals ---
cat "$WORK"/*.delete.txt 2>/dev/null | LC_ALL=C sort -u > "$WORK/all.delete.txt"
TOTAL=$(wc -l < "$WORK/all.delete.txt" | tr -d ' ')
say "total objects to delete: $TOTAL"

if [ "$TOTAL" -eq 0 ]; then
    say "nothing to do."
    exit 0
fi

if [ "$APPLY" != 1 ]; then
    rule
    say "report only; nothing was deleted."
    say "re-run with --apply to delete, or --keep-lists to inspect the lists first."
    exit 0
fi

# ----------------------------------------------------------------- deleting --
# One b2 invocation per object, in parallel: each one pays process startup and
# authentication before it makes an API call, so serially this is hours at the
# sizes involved rather than minutes.
rule
say "deleting $TOTAL objects with $JOBS workers; this is the slow part..."

rm_one() {
    b2 rm --quiet "$BASE/$1" >/dev/null 2>&1 && return 0
    b2 file rm "$BASE/$1" >/dev/null 2>&1 && return 0
    printf '%s\n' "$1"
}
export -f rm_one
export BASE

xargs -P "$JOBS" -I {} bash -c 'rm_one "$@"' _ {} \
    < "$WORK/all.delete.txt" > "$WORK/failed.txt"

FAILED=$(wc -l < "$WORK/failed.txt" | tr -d ' ')
if [ "$FAILED" -gt 0 ]; then
    say "objects that would not delete:"
    head -5 "$WORK/failed.txt" | sed 's/^/  /'
fi

say "done. deleted $((TOTAL - FAILED)), failed $FAILED"
[ "$FAILED" -eq 0 ]
