#!/bin/sh

set -eu

source_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin" "$test_root/dist" "$test_root/scripts"
cp "$source_root/scripts/spectre" "$test_root/scripts/spectre"

export SPECTRE_LAUNCHER_TEST_LOG="$test_root/log"
export PATH="$test_root/scripts:$test_root/bin:$PATH"
cat > "$test_root/bin/bit" <<'EOF'
#!/bin/sh
printf 'bit %s\n' "$*" >> "$SPECTRE_LAUNCHER_TEST_LOG"
EOF
chmod +x "$test_root/bin/bit"

check() {
  name=$1
  target=$2
  if [ "$name" != spectre ]; then
    ln -s spectre "$test_root/scripts/$name"
  fi
  cat > "$test_root/dist/$name" <<'EOF'
#!/bin/sh
printf '%s %s\n' "$(basename "$0")" "$*" >> "$SPECTRE_LAUNCHER_TEST_LOG"
EOF
  chmod +x "$test_root/dist/$name"
  : > "$SPECTRE_LAUNCHER_TEST_LOG"

  "$name" --flag 'two words'

  expected=$(printf 'bit -q %s\n%s --flag two words' "$target" "$name")
  actual=$(cat "$SPECTRE_LAUNCHER_TEST_LOG")
  if [ "$actual" != "$expected" ]; then
    printf '%s: expected:\n%s\ngot:\n%s\n' "$name" "$expected" "$actual" >&2
    exit 1
  fi
}

check spectre spectre-bin
check spectre-sample sample-bin
check spectre-sample-forecasts sample-forecasts-bin

printf 'Launcher test passed\n'
