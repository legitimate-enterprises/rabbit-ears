#!/usr/bin/env bash
# Prints `<stack>=true` or `<stack>=false` for every stack: whether anything
# its gates read differs from the last commit on main whose ci run was
# green. Comparing with that commit, not the one before this push, keeps a
# stack that went red from being skipped by a later push that leaves it
# alone. A path this does not know, or no green run to compare with, runs
# every stack.
set -euo pipefail
cd "$(dirname "$0")/.."

STACKS="server web"

all() {
    for s in $STACKS; do echo "$s=true"; done
    exit 0
}

# Push runs only: a pull request's run tests a merge, not the commit it names.
base="$(gh run list --workflow ci.yaml --branch main --event push --status success --limit 1 \
    --json headSha --jq '.[0].headSha // empty' 2>/dev/null)" || all
[ -n "$base" ] || all
# The checkout is shallow; one commit is all a comparison of trees needs.
git cat-file -e "$base^{commit}" 2>/dev/null || git fetch --quiet --depth=1 origin "$base" 2>/dev/null || all

run=" "
while IFS= read -r f; do
    case "$f" in
        # Read by no gate.
        README.md | AGENTS.md | LICENSE | .gitignore | scripts/cut-release.sh | .github/workflows/release.yaml \
            | .github/workflows/site.yaml | docs/site/*) ;;
        # The server embeds the web client, and the web client's generated
        # code is checked against the server's spec and its browser tests
        # run a server over the fixtures.
        server/* | web/* | scripts/fetch-fixtures.sh | scripts/verify-fixtures.sh | scripts/make-pgs.py) run="$run server web " ;;
        *) all ;;
    esac
done < <(git diff --no-renames --name-only "$base" HEAD)

for s in $STACKS; do
    case "$run" in
        *" $s "*) echo "$s=true" ;;
        *) echo "$s=false" ;;
    esac
done
