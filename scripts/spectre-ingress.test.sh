#!/bin/sh

set -eu

source_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin" "$test_root/cmd/spectre-sample" "$test_root/scripts" "$test_root/sync"
cp "$source_root/scripts/spectre-ingress" "$test_root/scripts/spectre-ingress"
ln -s spectre-ingress "$test_root/scripts/spectre-sample"

export SPECTRE_LAUNCHER_TEST_LOG="$test_root/builds"
export SPECTRE_LAUNCHER_TEST_SYNC="$test_root/sync"
cat > "$test_root/bin/go" <<'EOF'
#!/bin/sh
set -eu

output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    output=$2
    shift 2
    continue
  fi
  shift
done
printf '%s\n' "$output" >> "$SPECTRE_LAUNCHER_TEST_LOG"
: > "$SPECTRE_LAUNCHER_TEST_SYNC/$$"
while [ "$(find "$SPECTRE_LAUNCHER_TEST_SYNC" -type f | wc -l)" -lt 2 ]; do
  sleep 0.01
done
printf '#!/bin/sh\nexit 0\n' > "$output"
chmod +x "$output"
EOF
chmod +x "$test_root/bin/go"

"$test_root/scripts/spectre-sample" &
first=$!
"$test_root/scripts/spectre-sample" &
second=$!
wait "$first"
wait "$second"

shared="$test_root/dist/devel/spectre-sample"
if grep -Fqx "$shared" "$SPECTRE_LAUNCHER_TEST_LOG"; then
  printf 'concurrent builds used the shared output path\n' >&2
  exit 1
fi
if [ "$(sort -u "$SPECTRE_LAUNCHER_TEST_LOG" | wc -l)" -ne 2 ]; then
  printf 'concurrent builds did not use unique output paths\n' >&2
  exit 1
fi
if [ ! -x "$shared" ]; then
  printf 'launcher did not publish an executable\n' >&2
  exit 1
fi

printf 'Launcher concurrency regression test passed\n'
