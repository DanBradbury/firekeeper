#!/bin/sh
# Fails unless the modernc.org/libc version this module builds with is exactly
# the one modernc.org/sqlite's own go.mod requires. libc does not follow
# semver, so any other version can break sqlite at run time
# (https://gitlab.com/cznic/sqlite/-/issues/177).
#
# Usage: scripts/check-sqlite-libc.sh
#        scripts/check-sqlite-libc.sh SQLITE_GO_MOD LIBC_VERSION   (tests)
set -eu

# required_libc prints the modernc.org/libc version a go.mod file requires,
# in either the block form or the single-line "require" form.
required_libc() {
	awk '
		/^[ \t]*\/\// { next }
		$1 == "require" && $2 == "modernc.org/libc" { print $3; exit }
		$1 == "modernc.org/libc" { print $2; exit }
	' "$1"
}

if [ $# -eq 2 ]; then
	sqlite_mod=$1
	have=$2
	sqlite=$(basename "$sqlite_mod")
elif [ $# -eq 0 ]; then
	cd "$(dirname "$0")/.."
	sqlite=$(go list -m -f '{{.Version}}' modernc.org/sqlite)
	have=$(go list -m -f '{{.Version}}' modernc.org/libc)
	sqlite_mod=$(go mod download -json "modernc.org/sqlite@$sqlite" |
		sed -n 's/^[[:space:]]*"GoMod":[[:space:]]*"\(.*\)",*$/\1/p')
	if [ -z "$sqlite_mod" ] || [ ! -f "$sqlite_mod" ]; then
		echo "check-sqlite-libc: could not find the go.mod of modernc.org/sqlite@$sqlite" >&2
		exit 2
	fi
else
	echo "usage: $0 [SQLITE_GO_MOD LIBC_VERSION]" >&2
	exit 2
fi

want=$(required_libc "$sqlite_mod")
if [ -z "$want" ]; then
	echo "check-sqlite-libc: $sqlite_mod does not require modernc.org/libc" >&2
	exit 1
fi
if [ "$have" != "$want" ]; then
	echo "check-sqlite-libc: modernc.org/sqlite $sqlite requires modernc.org/libc $want exactly, but this module uses $have." >&2
	echo "Run: go get modernc.org/libc@$want && go mod tidy" >&2
	exit 1
fi
echo "modernc.org/sqlite $sqlite and modernc.org/libc $have match"
