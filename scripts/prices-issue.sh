#!/bin/sh
# Opens an issue titled $1 with body $2, or comments $2 on the open issue
# with that exact title, so repeated runs do not pile up issues. Used by the
# "Refresh default prices" workflow. Needs an authenticated gh (GH_TOKEN).
#
# Usage: scripts/prices-issue.sh TITLE BODY
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: $0 TITLE BODY" >&2
	exit 2
fi
title=$1
body=$2

number=$(gh issue list --state open --search "\"$title\" in:title" --json number,title \
	--jq "map(select(.title == \"$title\")) | first | .number // empty")
if [ -n "$number" ]; then
	gh issue comment "$number" --body "$body"
else
	gh issue create --title "$title" --body "$body"
fi
