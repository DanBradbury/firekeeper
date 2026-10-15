#!/bin/sh
# Reminds maintainers to refresh the default price table. When the table's
# top-level as_of is older than $MAX_AGE_DAYS (default 45), it opens a
# reminder issue, or comments on the open one. A fresh table does nothing.
#
# Run by the scheduled "Price table reminder" workflow, never by CI builds.
# Needs GNU date and an authenticated gh (GH_TOKEN). With --dry-run it only
# prints what it would do. See docs/prices.md.
set -eu
cd "$(dirname "$0")/.."

dry_run=false
if [ "${1:-}" = "--dry-run" ]; then
	dry_run=true
fi
max_age=${MAX_AGE_DAYS:-45}
table=internal/server/prices/defaults.toml
title="Refresh the default price table"

# The top-level as_of is the first one, before any [[model]] entry.
as_of=$(sed -n 's/^as_of = "\([0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]\)"$/\1/p' "$table" | head -n 1)
if [ -z "$as_of" ]; then
	echo "no as_of date found in $table" >&2
	exit 1
fi
age=$(( ($(date -u +%s) - $(date -u -d "$as_of" +%s)) / 86400 ))
if [ "$age" -le "$max_age" ]; then
	echo "default prices are $age days old (as of $as_of); limit $max_age"
	exit 0
fi

body="The default price table in \`$table\` is $age days old (as of $as_of), past the $max_age-day limit.

Refresh it by following docs/prices.md: reread each provider's pricing page, update the rates, \`source\` and \`as_of\` of every entry, and set the top-level \`as_of\` to the oldest entry's date."

if [ "$dry_run" = true ]; then
	echo "stale: would open or comment on \"$title\""
	echo "$body"
	exit 0
fi

number=$(gh issue list --state open --search "\"$title\" in:title" --json number,title \
	--jq "map(select(.title == \"$title\")) | first | .number // empty")
if [ -n "$number" ]; then
	gh issue comment "$number" --body "$body"
else
	gh issue create --title "$title" --body "$body"
fi
