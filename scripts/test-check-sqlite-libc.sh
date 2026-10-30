#!/bin/sh
# Tests check-sqlite-libc.sh against fixture go.mod files. No network or Go.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
check="$here/check-sqlite-libc.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

fails=0
expect() { # expect pass|fail NAME MOD LIBC
	if sh "$check" "$3" "$4" >"$work/out" 2>&1; then got=pass; else got=fail; fi
	if [ "$got" = "$1" ]; then
		printf 'ok   %s\n' "$2"
	else
		printf 'FAIL %s (wanted %s, got %s)\n' "$2" "$1" "$got"
		sed 's/^/     /' "$work/out"
		fails=$((fails + 1))
	fi
}

cat >"$work/block.mod" <<'MOD'
module modernc.org/sqlite

go 1.26.0

require (
	golang.org/x/sys v0.49.0
	// modernc.org/libc v1.1.0 is a comment, not a requirement
	modernc.org/libc v1.79.0
	modernc.org/mathutil v1.7.1
)

require modernc.org/libcx v9.9.9 // indirect
MOD
printf 'module modernc.org/sqlite\n\nrequire modernc.org/libc v1.77.1\n' >"$work/single.mod"
printf 'module modernc.org/sqlite\n\nrequire golang.org/x/sys v0.49.0\n' >"$work/none.mod"

expect pass "block require, exact match" "$work/block.mod" v1.79.0
expect fail "block require, newer libc" "$work/block.mod" v1.80.0
expect fail "block require, older libc" "$work/block.mod" v1.78.0
expect pass "single-line require, exact match" "$work/single.mod" v1.77.1
expect fail "single-line require, mismatch" "$work/single.mod" v1.79.0
expect fail "sqlite go.mod without libc" "$work/none.mod" v1.79.0

if [ "$fails" -ne 0 ]; then
	echo "$fails check(s) failed" >&2
	exit 1
fi
