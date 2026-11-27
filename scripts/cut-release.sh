#!/usr/bin/env bash
# Tags the server for release. Its version is the one server/api/openapi.yaml
# states on origin/main, so there is no bump to choose here: the tag is
# v<version>, cut on origin/main once ci is green there, and pushing it starts
# release.yaml. `make release` runs this with --yes.
#
# Usage: cut-release.sh                  the version and its state
#        cut-release.sh --yes            tag and push
#        cut-release.sh --check <tag>    fail unless the tag is v<the version HEAD states>
set -euo pipefail
cd "$(dirname "$0")/.."

SOURCE=server/api/openapi.yaml
# What ships in a release. The web client rides in the server binary.
PATHS=(server web)

die() { echo "error: $*" >&2; exit 1; }

# version_at <ref>
version_at() {
    local v form='^[0-9]+\.[0-9]+\.[0-9]+$'
    v="$(git show "$1:$SOURCE" | sed -n 's/^  version: *//p' | head -1)"
    [[ "$v" =~ $form ]] || die "$SOURCE at $1 states no version of the form $form (got '$v')"
    echo "$v"
}

# state: one line saying whether origin/main can be tagged, ending in the tag
# when it can. Returns 0 when it can, 1 when there is nothing to release, 2
# when the version must be put right first.
state() {
    local v tag newest
    v="$(version_at origin/main)" || return 2
    tag="v$v"
    if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null; then
        if git diff --quiet "$tag" origin/main -- "${PATHS[@]}"; then
            echo "$v is released and nothing in it has changed"
            return 1
        fi
        echo "changed since $tag but still says $v: raise the version in $SOURCE"
        return 2
    fi
    newest="$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//' | sort -V | tail -1)"
    if [ -n "$newest" ] && [ "$(printf '%s\n%s\n' "$newest" "$v" | sort -V | tail -1)" != "$v" ]; then
        echo "says $v, below the released $newest: raise the version in $SOURCE"
        return 2
    fi
    echo "$v is ready: $tag"
}

if [ "${1:-}" = "--check" ]; then
    tag="${2:?usage: cut-release.sh --check <tag>}"
    v="$(version_at HEAD)"
    [ "$tag" = "v$v" ] || die "$tag is not v$v, the version $SOURCE states at HEAD"
    exit 0
fi

[ $# -eq 0 ] || [ "$*" = "--yes" ] || die "usage: cut-release.sh [--yes | --check <tag>]"

git fetch origin main --tags --quiet

if [ $# -eq 0 ]; then
    state || true
    echo "usage: make release"
    exit 0
fi

line="$(state)" || die "nothing tagged: $line"
tag="${line##* }"

# A tag is a promise that its commit passed ci, so the check comes before
# the tag exists; release.yaml asserts it again.
sha="$(git rev-parse origin/main)"
echo "tagging origin/main at ${sha:0:8}: $(git log -1 --format=%s "$sha")"
command -v gh >/dev/null 2>&1 || die "gh is required to verify ci before tagging (brew install gh)"

# A run record appears a beat after the push that created it.
deadline=$((SECONDS + 120))
while :; do
    run="$(gh run list --workflow ci.yaml --branch main --commit "$sha" --limit 1 \
        --json databaseId,status,conclusion,url \
        --jq '.[0] // empty | [.databaseId, .status, .conclusion // "pending", .url] | @tsv')" ||
        die "could not query ci runs; check 'gh auth status'"
    [ -n "$run" ] && break
    [ "$SECONDS" -lt "$deadline" ] || die "no ci run for ${sha:0:8} on main after 120s; is the commit pushed?"
    sleep 5
done
IFS=$'\t' read -r run_id run_status run_conclusion run_url <<< "$run"

if [ "$run_status" != "completed" ]; then
    echo "waiting for ci on ${sha:0:8}: $run_url"
    gh run watch "$run_id" --exit-status || die "ci failed; no tag created: $run_url"
elif [ "$run_conclusion" != "success" ]; then
    die "ci concluded '$run_conclusion'; no tag created: $run_url"
fi

# main can move while ci runs; the tag belongs on the commit that run vouched for.
git fetch origin main --quiet
[ "$(git rev-parse origin/main)" = "$sha" ] || die "origin/main moved while ci ran; no tag created"

git tag --annotate "$tag" --message "$tag" "$sha"
git push origin "$tag"
echo "pushed $tag"
