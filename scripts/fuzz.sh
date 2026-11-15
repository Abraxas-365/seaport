#!/usr/bin/env bash
# Run every Go fuzz target for TIME each (default 10s): scripts/fuzz.sh 2m
#
# Targets are found with `go test -list`, so a new FuzzX is picked up with no
# change here. No database needed. A failing input is written to
# <pkg>/testdata/fuzz/<FuzzX>/ by Go: commit it as a regression test.
# Set FUZZ_PKGS to limit the packages (default ./...), FUZZ_SHARD=i/n to run
# only every n-th target starting at i (1-based): CI splits the work over
# parallel jobs that way.
set -euo pipefail

time="${1:-10s}"
pkgs="${FUZZ_PKGS:-./...}"
cd "$(dirname "$0")/.."

# "pkg-path FuzzName" lines. `go test -list` prints the names first and the
# "ok <pkg>" line after them, so pair each name with the next ok line.
targets=()
while IFS= read -r line; do
  targets+=("$line")
done < <(
  # shellcheck disable=SC2086
  go test -list '^Fuzz' $pkgs 2>/dev/null | awk '
    /^Fuzz/ { names[n++] = $1; next }
    /^ok[ \t]/ { for (i = 0; i < n; i++) print $2, names[i]; n = 0 }
  ' | sort
)

if [ -n "${FUZZ_SHARD:-}" ]; then
  i="${FUZZ_SHARD%/*}"
  n="${FUZZ_SHARD#*/}"
  if ! [[ "$i" =~ ^[0-9]+$ && "$n" =~ ^[0-9]+$ ]] || [ "$i" -lt 1 ] || [ "$i" -gt "$n" ]; then
    echo "FUZZ_SHARD must be i/n with 1 <= i <= n, got: $FUZZ_SHARD" >&2
    exit 2
  fi
  picked=()
  for k in "${!targets[@]}"; do
    if [ $((k % n + 1)) -eq "$i" ]; then
      picked+=("${targets[$k]}")
    fi
  done
  targets=("${picked[@]}")
fi

if [ "${#targets[@]}" -eq 0 ]; then
  echo "no fuzz targets found" >&2
  exit 1
fi

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

failed=0
for t in "${targets[@]}"; do
  pkg="${t% *}"
  name="${t#* }"
  echo "=== fuzz $name ($pkg) for $time"
  if go test -run='^$' -fuzz="^${name}\$" -fuzztime="$time" "$pkg" 2>&1 | tee "$log"; then
    continue
  fi
  # Go's fuzzer sometimes ends a clean run with "context deadline exceeded"
  # when -fuzztime runs out while a worker is busy (no input failed, none
  # written). A real failure always writes its input: that one fails.
  if grep -q 'context deadline exceeded' "$log" && ! grep -q 'Failing input written to' "$log"; then
    echo "WARN: $name ($pkg) stopped at the deadline with no failing input; not a failure" >&2
    continue
  fi
  echo "FAILED: $name ($pkg)" >&2
  failed=1
done
exit "$failed"
