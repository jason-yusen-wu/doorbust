#!/usr/bin/env bash
# Exports .env into the environment and execs the given command.
#
# Values are read literally (via `read`, not shell-eval'd) so DSNs containing
# shell metacharacters like `&` don't need quoting — the same unquoted
# KEY=VALUE format also works directly as `docker run --env-file .env`.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="$script_dir/../.env"

set -a
while IFS='=' read -r key value; do
	case "$key" in
	'' | '#'*) continue ;;
	esac
	export "$key=$value"
done <"$env_file"
set +a

exec "$@"
