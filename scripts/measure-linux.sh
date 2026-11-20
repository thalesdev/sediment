#!/usr/bin/env bash
# Measures the CPU that sedimentd uses for each command, in a container that has a
# limit of CPUs, on Linux. The CPU is the user plus system time that the server reports
# in INFO cpu (getrusage), so the script needs no access to the cgroup of the host, and
# it works the same on a Linux host and in a Linux virtual machine of a laptop.
#
#   scripts/measure-linux.sh                  # image sediment:dev, 1 CPU
#   CPUS=2 REQUESTS=2000000 scripts/measure-linux.sh
#   GOMAXPROCS=1 scripts/measure-linux.sh     # one P, as the older numbers in BENCHMARKS.md
#
# The Go runtime sets GOMAXPROCS from the limit of the container, but never below 2, so
# a limit of 1 CPU runs with 2 Ps unless GOMAXPROCS says otherwise.
#
# It needs docker and an image of sediment (make docker). The load comes from
# redis-benchmark of the image redis:7-alpine, in the network of the server container.
set -euo pipefail

IMAGE="${IMAGE:-sediment:dev}"
CPUS="${CPUS:-1}"
REQUESTS="${REQUESTS:-1000000}"
CLIENTS="${CLIENTS:-50}"
DATASIZE="${DATASIZE:-100}"
BENCH_IMAGE="${BENCH_IMAGE:-redis:7-alpine}"
NAME="sediment-measure-$$"

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

env_args=()
if [ -n "${GOMAXPROCS:-}" ]; then
  env_args=(-e "GOMAXPROCS=$GOMAXPROCS")
fi
docker run -d --name "$NAME" --cpus="$CPUS" ${env_args[@]+"${env_args[@]}"} "$IMAGE" >/dev/null
tool() {
  docker run --rm --network "container:$NAME" "$BENCH_IMAGE" "$@"
}
for _ in $(seq 1 60); do
  if tool redis-cli -p 6379 PING 2>/dev/null | grep -q PONG; then
    break
  fi
  sleep 0.5
done

info() {
  tool redis-cli -p 6379 INFO "$1" | tr -d '\r'
}
cpu_seconds() {
  info cpu | awk -F: '/^used_cpu_(sys|user):/ { total += $2 } END { printf "%.6f", total }'
}
procs="$(info runtime | awk -F: '/^gomaxprocs:/ { print $2 }')"
echo "image=$IMAGE cpus=$CPUS gomaxprocs=$procs clients=$CLIENTS requests=$REQUESTS datasize=$DATASIZE kernel=$(docker run --rm "$BENCH_IMAGE" uname -sr)"
printf '%-14s %12s %14s\n' workload requests/s us-cpu/cmd

run() {
  local label="$1" pipeline="$2" test="$3"
  local before after out rps
  before="$(cpu_seconds)"
  out="$(tool redis-benchmark -p 6379 -c "$CLIENTS" -n "$REQUESTS" -d "$DATASIZE" -P "$pipeline" -r 100000 -t "$test" --csv -q)"
  after="$(cpu_seconds)"
  rps="$(echo "$out" | awk -F, '$1 != "\"test\"" { gsub(/"/, "", $2); printf "%.0f", $2; exit }')"
  awk -v l="$label" -v b="$before" -v a="$after" -v n="$REQUESTS" -v r="$rps" \
    'BEGIN { printf "%-14s %12s %14.3f\n", l, r, (a - b) * 1e6 / n }'
}

run "set P1" 1 set
run "get P1" 1 get
run "set P16" 16 set
run "get P16" 16 get
run "set P64" 64 set
