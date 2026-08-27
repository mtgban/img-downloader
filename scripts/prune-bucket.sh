#!/usr/bin/env bash
#
# Delete the objects the mirror has superseded but never removed.
#
# The mirror has no deletion path in it at all: it writes an object at the path
# the current layout asks for and moves on, so every object left behind by a
# layout or format change is still there. This finds those and, with --apply,
# removes them.
#
# Nothing is deleted unless its replacement is present. That is the whole
# safety model here: an orphan is only an orphan because something newer took
# its place, so if the newer object is missing the old one is still the only
# copy and is left alone.
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
        -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
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

# ---------------------------------------------------------------- live tree --
# Everything below is justified by the current layout, so if the current layout
# is not there we are pointed at the wrong place and must not delete anything.
say "reading the live singles tree..."
list 'singles/grid/front/*.webp' > "$WORK/live-singles.txt"
LIVE_SINGLES=$(wc -l < "$WORK/live-singles.txt" | tr -d ' ')
if [ "$LIVE_SINGLES" -eq 0 ]; then
    echo "refusing to continue: $BASE/singles/grid/front/ holds no webp objects." >&2
    echo "either the base is wrong or the mirror has not run under the current layout." >&2
    exit 1
fi
say "  $LIVE_SINGLES live singles"

say "reading the live sealed tree..."
list 'sealed/*.webp' > "$WORK/live-sealed.txt"
say "  $(wc -l < "$WORK/live-sealed.txt" | tr -d ' ') live sealed"
rule

# The id is the basename without extension, and it is the only thing an old
# path and its replacement share; shard directories and variant segments differ.
ids() { sed -e 's|.*/||' -e 's|\.[^.]*$||'; }

# orphans <listfile> <replacements-listfile> <label>
# Splits an old-layout listing into what is safe to delete (its replacement
# exists) and what is not (it does not, so this is still the only copy).
#
# Done as two joins over sorted files rather than a lookup per object. The
# corpus is ~120k objects against ~120k ids, so the per-object form is O(n*m)
# and does not finish in any useful time.
orphans() {
    local old=$1 live=$2 label=$3
    local tab
    tab=$(printf '\t')

    ids < "$live" | LC_ALL=C sort -u > "$WORK/.live-ids"
    # "<id>\t<object>", sorted on the id, which is what both joins key on
    awk -F/ '{ n = $NF; sub(/\.[^.]*$/, "", n); print n "\t" $0 }' "$old" \
        | LC_ALL=C sort -t "$tab" -k1,1 > "$WORK/.old-by-id"

    LC_ALL=C join -t "$tab" -o 1.2 "$WORK/.old-by-id" "$WORK/.live-ids" \
        > "$WORK/$label.delete.txt"
    LC_ALL=C join -t "$tab" -v 1 -o 1.2 "$WORK/.old-by-id" "$WORK/.live-ids" \
        > "$WORK/$label.hold.txt"
}

report() {
    local label=$1 desc=$2
    local d h
    d=$(wc -l < "$WORK/$label.delete.txt" | tr -d ' ')
    h=$(wc -l < "$WORK/$label.hold.txt" 2>/dev/null | tr -d ' ' || echo 0)
    say "$desc"
    say "  to delete: $d"
    if [ "${h:-0}" -gt 0 ]; then
        say "  HELD BACK: $h (no replacement present - these are still the only copy)"
        head -3 "$WORK/$label.hold.txt" | sed 's/^/    /'
    fi
}

# ------------------------------------------------- 1. pre-grid singles (jpg) --
# PR #8 moved singles to singles/grid/front/<id>.webp; the old tree is dead.
say "scanning singles/front/ (pre-grid jpg)..."
list 'singles/front/*' > "$WORK/old-singles.txt"
orphans "$WORK/old-singles.txt" "$WORK/live-singles.txt" old-singles
report old-singles "singles/front/ - superseded by singles/grid/front/"
rule

# ------------------------------------ 2. scryfall-verbatim era, under normal/ --
# Before the singles/ rename the path was Scryfall's url verbatim.
say "scanning normal/ (scryfall-verbatim era)..."
list 'normal/*' > "$WORK/old-normal.txt"
orphans "$WORK/old-normal.txt" "$WORK/live-singles.txt" old-normal
report old-normal "normal/ - superseded by singles/grid/front/"
rule

# -------------------------------------------------------- 3. sealed jpg ------
# Sealed jpg and its webp replacement share a directory, so this can never be a
# prefix delete - it has to be matched object by object.
say "scanning sealed/ for jpg..."
list 'sealed/*.jpg' > "$WORK/old-sealed.txt"
orphans "$WORK/old-sealed.txt" "$WORK/live-sealed.txt" old-sealed
report old-sealed "sealed/*.jpg - superseded by sealed/*.webp"
rule

# ------------------------------------------------------ 4. stale bundles -----
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
