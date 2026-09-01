#!/usr/bin/env bash
# Enforces per-package coverage floors against a `go test -coverprofile` file.
#
# Floors are per package rather than one repo-wide number on purpose. A global
# figure is dominated by whatever has the most lines — here the generated sqlc
# code and the cmd wiring — so it can be met while the logic that actually
# decides whether stock is oversold goes untested, and can fail because of glue
# that is not worth testing. Packages with no floor are simply not listed.
set -euo pipefail

profile="${1:-coverage.out}"

if [ ! -f "$profile" ]; then
	echo "coverage profile not found: $profile" >&2
	exit 1
fi

# package:minimum percent
FLOORS="
internal/orders:85
internal/payments:85
internal/auth:90
internal/json:90
internal/products:80
internal/customers:80
"

module="$(go list -m)"

# `go tool cover -func` prints one line per function plus a total; the package
# is everything before the last slash of the file path.
coverage="$(go tool cover -func="$profile")"

printf '%-28s %8s %8s\n' "PACKAGE" "COVERAGE" "FLOOR"
printf '%-28s %8s %8s\n' "----------------------------" "--------" "-----"

failed=0

for entry in $FLOORS; do
	pkg="${entry%%:*}"
	floor="${entry##*:}"

	# Average the statement coverage of every function in the package. Summing
	# the per-function percentages is an approximation of the package total,
	# but a stable one, and it is what a reader sees in `-func` output.
	actual="$(
		echo "$coverage" \
			| grep "^${module}/${pkg}/" \
			| awk '{ gsub(/%/, "", $NF); total += $NF; n++ }
			       END { if (n == 0) print "none"; else printf "%.1f", total / n }'
	)"

	if [ "$actual" = "none" ]; then
		printf '%-28s %8s %8s  NO DATA\n' "$pkg" "-" "$floor%"
		echo "  no coverage recorded for $pkg — was it built with -coverpkg=./... ?" >&2
		failed=1
		continue
	fi

	if awk -v a="$actual" -v f="$floor" 'BEGIN { exit !(a < f) }'; then
		printf '%-28s %7s%% %7s%%  FAIL\n' "$pkg" "$actual" "$floor"
		shortfall="$(awk -v a="$actual" -v f="$floor" 'BEGIN { printf "%.1f", f - a }')"
		echo "  $pkg is ${shortfall} points below its floor of ${floor}%" >&2
		failed=1
	else
		printf '%-28s %7s%% %7s%%  ok\n' "$pkg" "$actual" "$floor"
	fi
done

total="$(echo "$coverage" | awk '/^total:/ { print $NF }')"
echo
echo "total (all packages, including generated and glue): $total"

if [ "$failed" -ne 0 ]; then
	echo
	echo "coverage floors not met" >&2
	exit 1
fi
