#!/usr/bin/env bash
# Signs in as a Cognito user via USER_PASSWORD_AUTH and prints the ID token,
# for curling protected doorbust endpoints by hand.
#
# Cognito identifiers come from .env (COGNITO_ISSUER_URL, COGNITO_CLIENT_ID)
# rather than being hardcoded here, so there's one place to change if the
# pool changes.
#
# COGNITO_CLIENT_SECRET is OPTIONAL, and for the current client id it should be
# unset. The app client the API verifies against (`doorbust-web`) is a PUBLIC
# client — it has no secret, because the SPA signs in with authorization code +
# PKCE from a browser, and Cognito rejects a SECRET_HASH from a client that has
# no secret. The variable is still honoured for the older confidential client
# (`doorbust`), which is why the branch exists.
#
# When it IS needed, read it only from the environment — never pass it as an
# argument, it would land in your shell history.
#
# Usage:
#   ./scripts/with-env.sh ./scripts/get-test-token.sh <username> <password>
set -euo pipefail

username="${1:-}"
password="${2:-}"

: "${COGNITO_ISSUER_URL:?not set — run this via ./scripts/with-env.sh so .env is loaded}"
: "${COGNITO_CLIENT_ID:?not set — run this via ./scripts/with-env.sh so .env is loaded}"

if [ -z "$username" ] || [ -z "$password" ]; then
	echo "usage: ./scripts/with-env.sh $0 <username> <password>" >&2
	exit 1
fi

# Issuer URL looks like https://cognito-idp.<region>.amazonaws.com/<pool-id>;
# the region is the sole thing the CLI needs beyond the client id.
region="$(printf '%s' "$COGNITO_ISSUER_URL" | sed -E 's#^https://cognito-idp\.([^.]+)\.amazonaws\.com/.*$#\1#')"
if [ "$region" = "$COGNITO_ISSUER_URL" ]; then
	echo "could not parse a region out of COGNITO_ISSUER_URL: $COGNITO_ISSUER_URL" >&2
	exit 1
fi

# A public client takes USERNAME/PASSWORD alone; sending a SECRET_HASH to one
# is an error rather than harmless extra input, so the parameter is omitted
# entirely unless a secret was supplied.
auth_params="USERNAME=${username},PASSWORD=${password}"
if [ -n "${COGNITO_CLIENT_SECRET:-}" ]; then
	secret_hash="$(printf '%s' "${username}${COGNITO_CLIENT_ID}" | openssl dgst -sha256 -hmac "$COGNITO_CLIENT_SECRET" -binary | base64)"
	auth_params="${auth_params},SECRET_HASH=${secret_hash}"
fi

aws cognito-idp initiate-auth \
	--region "$region" \
	--client-id "$COGNITO_CLIENT_ID" \
	--auth-flow USER_PASSWORD_AUTH \
	--auth-parameters "$auth_params" \
	--query 'AuthenticationResult.IdToken' \
	--output text
