#!/bin/sh
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binary=$(mktemp "${TMPDIR:-/tmp}/guigo-filter-test.XXXXXXXX")
trap 'rm -- "$binary"' EXIT
cc "$repo/tests/gtk3_filters.c" -o "$binary" $(pkg-config --cflags --libs gtk+-3.0) -ldl
"$binary"
