#!/usr/bin/env bash
# Starts the demo server cleanly, whatever was running before.
#
#   scripts/demo/server.sh              # normal settings
#   scripts/demo/server.sh --short-ttl  # 45s holds, for filming an expiry
#
# `make server` runs `go run`, which compiles to a binary in the Go build cache
# and execs it. Killing `go run` does not necessarily take that child with it —
# it gets re-parented to init and keeps holding port 8080, so the next start
# dies with "address already in use" and the stale server keeps answering. It
# looks like nothing changed, or like the site is down.
#
# Matching on a command name does not reliably find it either: the process shows
# up under its build-cache path, not as "go run" or "cmd". So this stops
# whatever holds the PORT, which is the thing that actually matters.
set -euo pipefail

cd "$(dirname "$0")/../.."

port=8080

holders="$(lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null || true)"
if [ -n "$holders" ]; then
	echo "stopping whatever holds :$port ($(echo "$holders" | tr '\n' ' '))"
	# SIGTERM first: the app drains in-flight requests on it, which is the same
	# signal a deploy sends.
	echo "$holders" | xargs kill 2>/dev/null || true
	for _ in $(seq 1 20); do
		lsof -ti "tcp:$port" -sTCP:LISTEN >/dev/null 2>&1 || break
		sleep 0.5
	done
	# Still there: it is not going to drain.
	remaining="$(lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null || true)"
	[ -n "$remaining" ] && echo "$remaining" | xargs kill -9 2>/dev/null || true
	sleep 1
fi

if [ "${1:-}" = "--short-ttl" ]; then
	export RESERVATION_TTL=45s RESERVATION_SWEEP_INTERVAL=10s
	echo "holds expire after 45s"
fi

log=/tmp/doorbust-demo.log
: > "$log"
./scripts/with-env.sh go run ./cmd >>"$log" 2>&1 &

printf 'starting'
for _ in $(seq 1 60); do
	if curl -fsS -m 2 "http://localhost:$port/health" >/dev/null 2>&1; then
		echo
		# Report the settings actually in force, so a stale server pretending to
		# be a fresh one is obvious rather than confusing.
		grep -oE 'reservation sweeper started interval=[^ ]+' "$log" | tail -1 || true
		echo "ready:  http://localhost:$port"
		echo "log:    $log"
		exit 0
	fi
	printf '.'
	sleep 1
done

echo
echo "did not come up — last lines of $log:" >&2
tail -15 "$log" >&2
exit 1
