#!/bin/sh
# Lands a refreshed default price table. Run by the "Refresh default
# prices" workflow after refresh-prices rewrote the table.
#
# It commits the table to the prices/refresh branch and opens a pull
# request, or updates the open one, with REPORT as its body. Pull requests
# opened with the workflow's token do not start CI on their own, so it
# dispatches CI on the branch, which reports the required checks on that
# commit. Unless REVIEW is "true", it then waits for CI and squash-merges.
# With REVIEW "true" a guardrail tripped: the pull request stays open for a
# person.
#
# Usage: scripts/prices-refresh-pr.sh REPORT REVIEW
# Needs git, an authenticated gh (GH_TOKEN, GH_REPO) and GNU date.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: $0 REPORT REVIEW" >&2
	exit 2
fi
report=$1
review=$2
cd "$(dirname "$0")/.."

branch=prices/refresh
table=internal/server/prices/defaults.toml
title="Refresh default prices ($(date -u +%Y-%m-%d))"

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git switch -C "$branch"
git add "$table"
git commit -m "$title"
git push --force origin "$branch"
sha=$(git rev-parse HEAD)

number=$(gh pr list --head "$branch" --state open --json number --jq '.[0].number // empty')
if [ -n "$number" ]; then
	gh pr edit "$number" --title "$title" --body-file "$report"
else
	gh pr create --base main --head "$branch" --title "$title" --body-file "$report"
	number=$(gh pr list --head "$branch" --state open --json number --jq '.[0].number')
fi

gh workflow run ci.yml --ref "$branch"

if [ "$review" = true ]; then
	gh pr comment "$number" --body "A guardrail tripped (see **Needs review** above), so this pull request will not merge itself. Check each listed change against its source page, then merge or close it."
	echo "pull request #$number needs review"
	exit 0
fi

# Wait for the dispatched CI run on this commit to appear, then to finish.
run=""
tries=0
while [ -z "$run" ]; do
	tries=$((tries + 1))
	if [ "$tries" -gt 30 ]; then
		echo "CI did not start on $sha" >&2
		exit 1
	fi
	sleep 10
	run=$(gh run list --workflow ci.yml --branch "$branch" --commit "$sha" --event workflow_dispatch --json databaseId --jq '.[0].databaseId // empty')
done
gh run watch "$run" --exit-status --interval 30
gh pr merge "$number" --squash --delete-branch --match-head-commit "$sha"
echo "merged pull request #$number"
