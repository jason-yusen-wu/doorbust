#!/usr/bin/env bash
# Signs in as a Cognito user via USER_PASSWORD_AUTH and prints the ID token,
# for curling protected doorbust endpoints by hand.
#
# Cognito identifiers come from .env (COGNITO_ISSUER_URL, COGNITO_CLIENT_ID)
# rather than being hardcoded here, so there's one place to change if the
# pool changes. The app client's secret is read only from the environment —
# never pass it as an argument, it would land in your shell history.
#
# Usage:
#   COGNITO_CLIENT_SECRET='...' ./scripts/with-env.sh ./scripts/get-test-token.sh <username> <password>
set -euo pipefail

username="${1:-}"
password="${2:-}"

: "${COGNITO_ISSUER_URL:?not set — run this via ./scripts/with-env.sh so .env is loaded}"
: "${COGNITO_CLIENT_ID:?not set — run this via ./scripts/with-env.sh so .env is loaded}"
: "${COGNITO_CLIENT_SECRET:?set COGNITO_CLIENT_SECRET in your environment first}"

if [ -z "$username" ] || [ -z "$password" ]; then
	echo "usage: COGNITO_CLIENT_SECRET='...' ./scripts/with-env.sh $0 <username> <password>" >&2
	exit 1
fi

# Issuer URL looks like https://cognito-idp.<region>.amazonaws.com/<pool-id>;
# the region is the sole thing the CLI needs beyond the client id.
region="$(printf '%s' "$COGNITO_ISSUER_URL" | sed -E 's#^https://cognito-idp\.([^.]+)\.amazonaws\.com/.*$#\1#')"
if [ "$region" = "$COGNITO_ISSUER_URL" ]; then
	echo "could not parse a region out of COGNITO_ISSUER_URL: $COGNITO_ISSUER_URL" >&2
	exit 1
fi

secret_hash="$(printf '%s' "${username}${COGNITO_CLIENT_ID}" | openssl dgst -sha256 -hmac "$COGNITO_CLIENT_SECRET" -binary | base64)"

aws cognito-idp initiate-auth \
	--region "$region" \
	--client-id "$COGNITO_CLIENT_ID" \
	--auth-flow USER_PASSWORD_AUTH \
	--auth-parameters USERNAME="$username",PASSWORD="$password",SECRET_HASH="$secret_hash" \
	--query 'AuthenticationResult.IdToken' \
	--output text
