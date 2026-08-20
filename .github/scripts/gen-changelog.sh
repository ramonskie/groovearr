#!/usr/bin/env bash
# Builds a release changelog from conventional commits between the previous
# release tag and the target commit. Used by .github/workflows/release.yml
# because GitHub's auto-generated notes (--generate-notes) come back empty when
# commits are pushed directly to main without pull requests.
#
# Env overrides (for backfilling past releases):
#   CHANGELOG_PREV   previous release tag (default: latest release, else root)
#   CHANGELOG_HEAD   upper bound commit (default: HEAD)
set -euo pipefail

REPO="${GITHUB_REPOSITORY:-ramonskie/groovearr}"
OUT="${1:-/tmp/release-notes.md}"
UPPER="${CHANGELOG_HEAD:-HEAD}"

if [ -n "${CHANGELOG_PREV:-}" ]; then
  PREV="$CHANGELOG_PREV"
else
  PREV="$(gh api "repos/${REPO}/releases/latest" --jq '.tag_name' 2>/dev/null || true)"
  if [ -z "$PREV" ] || [ "$PREV" = "$GITHUB_REF_NAME" ]; then
    PREV="$(git rev-list --max-parents=0 HEAD)"
  fi
fi

KNOWN="^(feat|fix|refactor|perf|docs?|build|ci|chore|improve)[(: ]"
body=""

append_section() {
  local label="$1" regex="$2"
  local items
  items="$(git log --no-merges --format='%s%x09%H' "$PREV".."$UPPER" | grep -iE "$regex" || true)"
  if [ -n "$items" ]; then
    body+="## ${label}\n\n"
    while IFS= read -r line; do
      subj="${line%%$'\t'*}"
      sha="${line##*$'\t'}"
      body+="- ${subj} ([${sha:0:7}](https://github.com/${REPO}/commit/${sha}))\n"
    done <<< "$items"
    body+="\n"
  fi
}

append_section "New features" "^(feat|feature)[(: ]"
append_section "Bug fixes" "^(fix|bugfix)[(: ]"
append_section "Refactors & improvements" "^(refactor|perf|improve)[(: ]"
append_section "Documentation" "^(docs?|readme)[(: ]"
append_section "Build & CI" "^(build|ci|chore|deps)[(: ]"

others="$(git log --no-merges --format='%s%x09%H' "$PREV".."$UPPER" | grep -ivE "$KNOWN" || true)"
if [ -n "$others" ]; then
  body+="## Other changes\n\n"
  while IFS= read -r line; do
    subj="${line%%$'\t'*}"
    sha="${line##*$'\t'}"
    body+="- ${subj} ([${sha:0:7}](https://github.com/${REPO}/commit/${sha}))\n"
  done <<< "$others"
  body+="\n"
fi

body+="**Full Changelog**: https://github.com/${REPO}/compare/${PREV}...${GITHUB_REF_NAME:-$UPPER}\n"
printf '%b' "$body" > "$OUT"
