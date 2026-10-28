#!/bin/bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# Hooks export repository-local Git variables; clear them for the nested repository.
while IFS= read -r variable; do
  unset "$variable"
done < <(git rev-parse --local-env-vars)
testdir="$(mktemp -d)"
trap 'rm -rf "$testdir"' EXIT

cp "$root/.svu.yml" "$testdir/.svu.yml"
git -C "$testdir" init -q
git -C "$testdir" config user.name "Release Test"
git -C "$testdir" config user.email "release-test@example.invalid"
mkdir "$testdir/hooks"
git -C "$testdir" config core.hooksPath "$testdir/hooks"

commit_change() {
  local message="$1"
  printf '%s\n' "$message" >> "$testdir/changes"
  git -C "$testdir" add changes
  git -C "$testdir" commit -qm "$message"
}

expect_version() {
  local expected="$1"
  local actual
  actual="$(cd "$testdir" && svu next)"
  if [[ "$actual" != "$expected" ]]; then
    printf 'expected version %q, got %q\n' "$expected" "$actual" >&2
    exit 1
  fi
}

commit_change "chore: establish baseline"
git -C "$testdir" tag v0.2.3

# The release workflow skips a release when the next version is unchanged.
commit_change "docs: clarify usage"
expect_version "v0.2.3"
commit_change "chore: update tooling"
expect_version "v0.2.3"
commit_change "custom: internal maintenance"
expect_version "v0.2.3"

commit_change "fix: correct output"
expect_version "v0.2.4"
git -C "$testdir" tag v0.2.4

commit_change "feat: add comparison mode"
expect_version "v0.3.0"
git -C "$testdir" tag v0.3.0

commit_change "feat!: replace comparison format"
expect_version "v0.4.0"
git -C "$testdir" tag v0.4.0

commit_change "fix: correct output

BREAKING CHANGE: output differs"
expect_version "v0.5.0"
git -C "$testdir" tag v0.5.0

# Leaving v0 needs a manual tag; after that, breaking changes bump major.
git -C "$testdir" tag v1.0.0

commit_change "chore!: replace release format"
expect_version "v2.0.0"

printf 'Release version regression tests passed\n'
