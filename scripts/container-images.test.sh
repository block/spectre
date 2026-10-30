#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
images="$script_dir/container-images"
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin"
log="$test_root/log"

for stub in docker bit gh curl; do
  cat > "$test_root/bin/$stub" <<'EOF'
#!/bin/sh
set -eu

printf '%s' "$(basename "$0")" >> "$PUBLISH_TEST_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$PUBLISH_TEST_LOG"
done
printf '\n' >> "$PUBLISH_TEST_LOG"

case "$(basename "$0") $*" in
  "docker buildx imagetools inspect --raw "*)
    case " ${PUBLISHED_IMAGES:-} " in
      *" $5 "*) printf '{"manifests":[{"digest":"sha256:amd64"},{"digest":"sha256:arm64"}]}\n' ;;
      *) exit 1 ;;
    esac
    ;;
  "docker buildx imagetools inspect --format "*) printf '{"digest":"sha256:index"}\n' ;;
  "gh api --paginate "*) printf '1 sha256:index\n2 sha256:amd64\n3 sha256:arm64\n4 sha256:other\n' ;;
  "curl -fsS -u "*) printf '{"token":"test-key-do-not-use"}\n' ;;
esac
EOF
  chmod +x "$test_root/bin/$stub"
done

export PATH="$test_root/bin:$PATH"
export PUBLISH_TEST_LOG="$log"
export DOCKER_HUB_USERNAME=user DOCKER_HUB_TOKEN=secret

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
"$images" publish v1.2.3 >/dev/null
assert_count 2 'bit <publish-container> <version=v1.2.3>'
assert_count 1 '<registry=ghcr.io/block> <image-tag=1.2.3>'
assert_count 1 '<registry=docker.io/blockossreleases> <image-tag=1.2.3>'
assert_count 0 'docker '

: > "$log"
"$images" promote v1.2.3 >/dev/null
assert_count 0 'bit '
assert_count 6 'docker <buildx> <imagetools> <create>'
assert_count 1 '<--tag> <ghcr.io/block/spectre:1.2> <ghcr.io/block/spectre:1.2.3>'
assert_count 1 '<--tag> <ghcr.io/block/spectre:1> <ghcr.io/block/spectre:1.2.3>'
assert_count 1 '<--tag> <ghcr.io/block/spectre:latest> <ghcr.io/block/spectre:1.2.3>'
assert_count 1 '<--tag> <docker.io/blockossreleases/spectre:1.2> <docker.io/blockossreleases/spectre:1.2.3>'
assert_count 1 '<--tag> <docker.io/blockossreleases/spectre:1> <docker.io/blockossreleases/spectre:1.2.3>'
assert_count 1 '<--tag> <docker.io/blockossreleases/spectre:latest> <docker.io/blockossreleases/spectre:1.2.3>'

: > "$log"
"$images" promote v1.2.3-rc.1 >/dev/null
assert_count 0 'docker '

: > "$log"
PUBLISHED_IMAGES='ghcr.io/block/spectre:1.2.3 docker.io/blockossreleases/spectre:1.2.3' \
  "$images" delete v1.2.3 >/dev/null
assert_count 3 'gh <api> <--method> <DELETE>'
assert_count 1 '<DELETE> </orgs/block/packages/container/spectre/versions/1>'
assert_count 1 '<DELETE> </orgs/block/packages/container/spectre/versions/2>'
assert_count 1 '<DELETE> </orgs/block/packages/container/spectre/versions/3>'
assert_count 1 '<-u> <user:secret>'
assert_count 3 'curl <-fsS> <-X> <DELETE> <-H> <Authorization: Bearer test-key-do-not-use>' # sadscan:disable np.http.2
assert_count 1 '<https://registry-1.docker.io/v2/blockossreleases/spectre/manifests/sha256:index>'
assert_count 1 '<https://registry-1.docker.io/v2/blockossreleases/spectre/manifests/sha256:amd64>'
assert_count 1 '<https://registry-1.docker.io/v2/blockossreleases/spectre/manifests/sha256:arm64>'

: > "$log"
PUBLISHED_IMAGES='' "$images" delete v1.2.3 >/dev/null
assert_count 0 'gh '
assert_count 0 'curl '

printf 'Container image script regression tests passed\n'
