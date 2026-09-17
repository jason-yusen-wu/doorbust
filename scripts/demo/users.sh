#!/usr/bin/env bash
# Creates (or resets) the two demo buyers used for the oversell race.
#
# Two accounts rather than one, because the race should read as two people
# wanting the same thing — not as one person clicking twice.
#
# The password is never stored in this repo. Supply it:
#
#   DEMO_PASSWORD='...' scripts/demo/users.sh
#
# or let the script generate one and print it once. Cognito requires at least 8
# characters with upper, lower, a digit and a symbol.
set -euo pipefail

pool="${COGNITO_USER_POOL_ID:-us-east-2_4r4djeEWS}"
password="${DEMO_PASSWORD:-}"

if [ -z "$password" ]; then
	# Generated rather than defaulted to something memorable: a fixed default
	# committed here would be a working credential for anyone reading the repo.
	password="Demo$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 12)!7"
	generated=1
fi

for user in demo-alice demo-bob; do
	aws cognito-idp admin-create-user \
		--user-pool-id "$pool" --username "$user" \
		--user-attributes Name=email,Value="$user@example.com" Name=email_verified,Value=true \
		--message-action SUPPRESS >/dev/null 2>&1 || true

	# Permanent, so Cognito does not demand a password change on camera.
	aws cognito-idp admin-set-user-password \
		--user-pool-id "$pool" --username "$user" \
		--password "$password" --permanent >/dev/null

	echo "ready: $user"
done

if [ -n "${generated:-}" ]; then
	echo
	echo "Password for both accounts (not stored anywhere — save it now):"
	echo "    $password"
fi
