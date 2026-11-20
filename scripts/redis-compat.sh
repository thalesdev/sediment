#!/usr/bin/env bash
# Runs files of the official Redis test suite against sedimentd and prints how many
# tests pass in each. The suite is the Tcl one in the tests directory of the Redis
# repository, which can run against a server that already runs (--host and --port).
#
#   scripts/redis-compat.sh                       # the files in FILES, below
#   FILES="unit/type/string unit/expire" scripts/redis-compat.sh
#   REDIS_TAG=8.0.0 scripts/redis-compat.sh
#   LENIENT=1 scripts/redis-compat.sh             # CONFIG SET does nothing and answers OK
#
# It needs git, tclsh (8.5 or newer) and go. It clones the Redis repository once, at
# REDIS_TAG, and applies scripts/redis-compat/harness.patch to the copy: the harness
# of an external server resets the server with commands that sediment does not have
# (FUNCTION, SCRIPT, CONFIG RESETSTAT, CONFIG GET *), and the patch removes those
# calls and nothing else. With LENIENT=1 it also applies scripts/redis-compat/lenient.patch,
# which makes CONFIG SET do nothing and answer OK and, with the options --ignore-encoding and --ignore-digest of the suite, skips the checks of the encoding and of the digest of a value, because many files start with them. Each file runs against a new server with an empty data
# directory, and its whole output goes to OUT/<file>.out.
#
# A test counts as passed only for [ok]. [err] is a test that failed, [exception] is a
# file that stopped at a command that sediment does not have, and [skip] is a test
# that the suite skips against an external server. The percentage is of the tests
# that ran, so a file that stops early looks better than it is: read the table with
# the column of exceptions. docs/REDIS-COMPATIBILITY.md explains the numbers.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
REDIS_TAG="${REDIS_TAG:-8.10.3}"
REDIS_SRC="${REDIS_SRC:-${TMPDIR:-/tmp}/sediment-redis-tests}"
OUT="${OUT:-${TMPDIR:-/tmp}/sediment-redis-compat-out}"
PORT="${PORT:-6399}"
LIMIT="${LIMIT:-600}"
FILES="${FILES:-unit/type/string unit/type/incr unit/type/hash unit/type/set unit/expire unit/keyspace unit/multi unit/scan unit/auth unit/acl unit/info unit/info-command unit/introspection unit/introspection-2 unit/protocol unit/quit unit/slowlog unit/limits unit/other unit/dump unit/networking unit/querybuf}"

mkdir -p "$OUT"
if [ ! -d "$REDIS_SRC/.git" ]; then
  git clone --quiet --depth 1 --branch "$REDIS_TAG" https://github.com/redis/redis.git "$REDIS_SRC"
fi
patches=("$root/scripts/redis-compat/harness.patch")
runtest_flags=()
if [ -n "${LENIENT:-}" ]; then
  patches+=("$root/scripts/redis-compat/lenient.patch")
  export SEDIMENT_LENIENT=1
  runtest_flags=(--ignore-encoding --ignore-digest)
fi
for patch in "${patches[@]}"; do
  if git -C "$REDIS_SRC" apply --check "$patch" 2>/dev/null; then
    git -C "$REDIS_SRC" apply "$patch"
  elif ! git -C "$REDIS_SRC" apply -R --check "$patch" 2>/dev/null; then
    echo "the patch $patch does not apply to $REDIS_SRC; remove the directory and run again" >&2
    exit 1
  fi
done

bin="$OUT/sedimentd"
(cd "$root/cmd/sedimentd" && go build -o "$bin" .)

run_limit=()
if command -v timeout >/dev/null 2>&1; then
  run_limit=(timeout "$LIMIT")
fi

server_pid=""
stop_server() {
  if [ -n "$server_pid" ]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
    server_pid=""
  fi
}
trap stop_server EXIT

# listening says if something accepts connections on the port of this machine.
listening() {
  (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

# free_port prints the first port from $1 up that nothing listens on.
free_port() {
  local p="$1"
  while listening "$p"; do
    p=$((p + 1))
  done
  echo "$p"
}

# count prints how many lines of the file start with the mark, after the colors of
# the harness are removed. The harness paints [ok] and [err] when it runs in a terminal.
count() {
  tr -d '\033' <"$2" | sed 's/\[[0-9;]*m//g' | grep -c "^\[$1\]" || true
}

echo "redis tests $REDIS_TAG, $(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)"
printf '%-26s %6s %6s %6s %6s %9s\n' file ok err except skip pass%
total_ok=0
total_err=0
for f in $FILES; do
  data="$(mktemp -d)"
  port="$(free_port "$PORT")"
  "$bin" -dir "$data" -port "$port" >"$OUT/server.log" 2>&1 &
  server_pid=$!
  started=0
  for _ in $(seq 1 100); do
    if ! kill -0 "$server_pid" 2>/dev/null; then
      break
    fi
    if listening "$port"; then
      started=1
      break
    fi
    sleep 0.1
  done
  if [ "$started" -ne 1 ]; then
    echo "sedimentd did not start on port $port:" >&2
    cat "$OUT/server.log" >&2
    exit 1
  fi
  out="$OUT/$(echo "$f" | tr / _).out"
  (cd "$REDIS_SRC" && ${run_limit[@]+"${run_limit[@]}"} ./runtest --host 127.0.0.1 --port "$port" --singledb --single "$f" --durable --clients 1 ${runtest_flags[@]+"${runtest_flags[@]}"} >"$out" 2>&1) || true
  stop_server
  rm -r "$data"
  ok=$(count ok "$out")
  err=$(count err "$out")
  exc=$(count exception "$out")
  skip=$(count skip "$out")
  ran=$((ok + err))
  pct="-"
  if [ "$ran" -gt 0 ]; then
    pct=$(LC_ALL=C awk -v o="$ok" -v r="$ran" 'BEGIN { printf "%.1f", 100 * o / r }')
  fi
  printf '%-26s %6d %6d %6d %6d %9s\n' "$f" "$ok" "$err" "$exc" "$skip" "$pct"
  total_ok=$((total_ok + ok))
  total_err=$((total_err + err))
done
all=$((total_ok + total_err))
if [ "$all" -gt 0 ]; then
  LC_ALL=C awk -v o="$total_ok" -v a="$all" 'BEGIN { printf "%-26s %6d %6d %6s %6s %9.1f\n", "total", o, a - o, "", "", 100 * o / a }'
fi
echo "output of each file: $OUT"
