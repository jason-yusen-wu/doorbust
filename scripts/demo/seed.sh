#!/usr/bin/env bash
# Seeds the demo catalogue.
#
#   scripts/demo/seed.sh                 # the database in .env (local)
#   scripts/demo/seed.sh "$OTHER_DSN"    # somewhere else
#
# Safe to re-run: seed.sql only deletes rows it created itself.
set -euo pipefail

dsn="${1:-}"
if [ -z "$dsn" ]; then
	if [ ! -f .env ]; then
		echo "no .env and no connection string given" >&2
		exit 1
	fi
	dsn="$(grep '^GOOSE_DBSTRING=' .env | cut -d= -f2-)"
fi

here="$(cd "$(dirname "$0")" && pwd)"
psql "$dsn" -v ON_ERROR_STOP=1 -f "$here/seed.sql"
