#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
publisher="$script_dir/publish-container-images"
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin"
log="$test_root/log"

cat > "$test_root/bin/docker" <<'EOF'
#!/bin/sh
set -eu

printf 'docker' >> "$PUBLISH_TEST_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$PUBLISH_TEST_LOG"
done
printf '\n' >> "$PUBLISH_TEST_LOG"

if [ "$1" = buildx ] && [ "$2" = imagetools ] && [ "$3" = inspect ]; then
  case " ${PUBLISHED_IMAGES:-} " in
    *" $4 "*) exit 0 ;;
    *) exit 1 ;;
  esac
fi
EOF
chmod +x "$test_root/bin/docker"

cat > "$test_root/bin/bit" <<'EOF'
#!/bin/sh
set -eu

printf 'bit' >> "$PUBLISH_TEST_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$PUBLISH_TEST_LOG"
done
printf '\n' >> "$PUBLISH_TEST_LOG"
EOF
chmod +x "$test_root/bin/bit"

export PATH="$test_root/bin:$PATH"
export PUBLISH_TEST_LOG="$log"

assert_count() {
  expected=$1
  pattern=$2
  actual=$(grep -Fc "$pattern" "$log" || true)
  if [ "$actual" -ne "$expected" ]; then
    printf 'expected %s occurrences of %s, got %s\n' "$expected" "$pattern" "$actual" >&2
    cat "$log" >&2
    exit 1
  fi
}

: > "$log"
PUBLISHED_IMAGES='' "$publisher" v1.2.3 >/dev/null
assert_count 2 'bit <-P> <version=v1.2.3>'
assert_count 2 '<image-tag=1.2.3>'
assert_count 6 'docker <buildx> <imagetools> <create>'
assert_count 1 '<--tag> <ghcr.io/block/spectre:1.2> <ghcr.io/block/spectre:1.2.3>'
assert_count 1 '<--tag> <ghcr.io/block/spectre:1> <ghcr.io/block/spectre:1.2.3>'
assert_count 1 '<--tag> <ghcr.io/block/spectre:latest> <ghcr.io/block/spectre:1.2.3>'
assert_count 1 '<--tag> <docker.io/blockossreleases/spectre:1.2> <docker.io/blockossreleases/spectre:1.2.3>'
assert_count 1 '<--tag> <docker.io/blockossreleases/spectre:1> <docker.io/blockossreleases/spectre:1.2.3>'
assert_count 1 '<--tag> <docker.io/blockossreleases/spectre:latest> <docker.io/blockossreleases/spectre:1.2.3>'

: > "$log"
PUBLISHED_IMAGES='ghcr.io/block/spectre:1.2.3 docker.io/blockossreleases/spectre:1.2.3' \
  "$publisher" v1.2.3 >/dev/null
assert_count 0 'bit '
assert_count 6 'docker <buildx> <imagetools> <create>'

: > "$log"
PUBLISHED_IMAGES='ghcr.io/block/spectre:1.2.3' "$publisher" v1.2.3 >/dev/null
assert_count 0 '<registry=ghcr.io/block>'
assert_count 1 '<registry=docker.io/blockossreleases>'
assert_count 6 'docker <buildx> <imagetools> <create>'

: > "$log"
PUBLISHED_IMAGES='' "$publisher" v1.2.3-rc.1 >/dev/null
assert_count 2 'bit <-P> <version=v1.2.3-rc.1>'
assert_count 0 'docker <buildx> <imagetools> <create>'

printf 'Container publisher regression tests passed\n'
