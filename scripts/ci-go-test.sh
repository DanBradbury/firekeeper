#!/bin/sh
# Runs the Go tests for CI's Linux job and fails if any test skipped. In CI a
# test that needs a missing tool fails on its own (internal/testenv), so a
# skip here means a test is gated in a way CI never exercises. The few tests
# that are meant to skip are listed in $allowed below.
#
# Usage: scripts/ci-go-test.sh [go test flags and packages], default
# "-race ./...". Needs jq. Prints package results, build errors, and the
# output of failed tests, not every test's output.
set -eu
cd "$(dirname "$0")/.."

# "package test" pairs that skip by design: a subprocess helper that only
# runs as a child of another test, and an opt-in benchmark.
allowed='github.com/DanBradbury/firekeeper/internal/daemon TestHelperDaemon
github.com/DanBradbury/firekeeper/internal/server/store TestUsageSpeed'

if [ "$#" -eq 0 ]; then
	set -- -race ./...
fi
results=$(mktemp)
trap 'rm -f "$results"' EXIT

status=0
go test -json "$@" >"$results" || status=$?

jq -rj 'select((.Action == "output" and .Test == null) or .Action == "build-output") | .Output' "$results"
if [ "$status" -ne 0 ]; then
	echo
	echo "Output of failed tests:"
	jq -rjs '
		[.[] | select(.Action == "fail" and .Test != null) | [.Package, .Test]] as $failed
		| .[] | select(.Action == "output" and .Test != null)
		| select([.Package, .Test] as $key | $failed | index([$key]) != null)
		| .Output' "$results"
	exit "$status"
fi

skipped=$(jq -r 'select(.Action == "skip" and .Test != null) | "\(.Package) \(.Test)"' "$results" |
	grep -v -x -F -e "$allowed" || true)
if [ -n "$skipped" ]; then
	echo "Tests skipped in CI. Make them run, or add them to the allowed list in $0:" >&2
	echo "$skipped" >&2
	exit 1
fi
