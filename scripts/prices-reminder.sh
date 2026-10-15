#!/bin/sh
# Opens a reminder issue, or comments on the open one, when the default
# price table's top-level as_of is older than $MAX_AGE_DAYS (default 45). A
# fresh table does nothing.
#
# The weekly refresh normally keeps the date current, so this only fires
# when refreshes keep failing or a refresh pull request waits for review
# too long. Run by the "Refresh default prices" workflow. Needs GNU date and
# an authenticated gh (GH_TOKEN). With --dry-run it only prints what it
# would do. See docs/prices.md.
set -eu
cd "$(dirname "$0")/.."

dry_run=false
if [ "${1:-}" = "--dry-run" ]; then
	dry_run=true
fi
max_age=${MAX_AGE_DAYS:-45}
table=internal/server/prices/defaults.toml
title="Default price table is out of date"

# The top-level as_of is the first one, before any [[model]] entry.
as_of=$(sed -n 's/^as_of = "\([0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]\)"$/\1/p' "$table" | head -n 1)
if [ -z "$as_of" ]; then
	echo "no as_of date found in $table" >&2
	exit 1
fi
age=$(( ($(date -u +%s) - $(date -u -d "$as_of" +%s)) / 86400 ))
if [ "$age" -le "$max_age" ]; then
	echo "default prices were confirmed $age day(s) ago (as of $as_of); limit $max_age"
	exit 0
fi

body="The default price table in \`$table\` was last confirmed $age days ago (as of $as_of), past the $max_age-day limit, so the weekly refresh has not landed a new date. Check for an open \"Refresh default prices\" pull request waiting for review, or an \"Automatic price refresh failed\" issue. See docs/prices.md."

if [ "$dry_run" = true ]; then
	echo "stale: would open or comment on \"$title\""
	echo "$body"
	exit 0
fi
scripts/prices-issue.sh "$title" "$body"
