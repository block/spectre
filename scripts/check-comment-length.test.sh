#!/bin/sh

set -eu

checker="$(dirname "$0")/check-comment-length"
testdir=$(mktemp -d)
trap 'rm -f "$testdir/input.go" "$testdir/output"; rmdir "$testdir"' EXIT

check() {
  name=$1
  expected=$2
  source=$3
  start=${4:-1}
  printf '%s\n' "$source" > "$testdir/input.go"
  actual=0
  "$checker" "$testdir/input.go" > "$testdir/output" 2>&1 || actual=$?
  output=$(cat "$testdir/output")
  expected_output=
  if [ "$expected" -ne 0 ]; then
    expected_output="$testdir/input.go:$start: comment exceeds two lines"
  fi
  if [ "$actual" -ne "$expected" ] || [ "$output" != "$expected_output" ]; then
    printf '%s: expected status %s, got %s\n%s\n' "$name" "$expected" "$actual" "$output" >&2
    exit 1
  fi
}

check VersionDirective 0 '// Version of SPECTRE.
//
//nolint:gochecknoglobals
var Version = "dev"'

check BlankCommentLines 0 '// First line.
//
//   
// Second line.'

check GoDirectives 0 '// First line.
//go:generate tool
//go:build linux
//line source.go:1
//extern symbol
//export Symbol
//lint:ignore rule reason
// Second line.'

check ExclusionsDoNotResetCount 1 '// First line.
//
//go:generate tool
// Second line.
// Third line.'

check SpacedDirectiveIsProse 1 '// First line.
// go:generate is described here.
// Third line.'

check LeadingExclusions 1 '//
//go:generate tool
// First line.
// Second line.
// Third line.' 3

check BlockBlanks 0 '/*
 * First line.
 *

 * Second line.
 */'

check BlockTooLong 1 '/* First line.

 * Second line.
 * Third line. */'

check DirectiveInsideBlockIsProse 1 '/* First line.
//go:generate tool
Third line. */'

check SeparateComments 0 '// First line.
// Second line.
var first = 1
// First line.
// Second line.'

printf 'Comment-length regression tests passed\n'
