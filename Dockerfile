# The storefront ships inside this image and is served by the Go binary at the
# same origin as the API. That is not a packaging preference: the EC2 box has no
# Elastic IP, no domain, no TLS and a security group admitting one /32, so a
# browser cannot reach :8080 from anywhere a CDN-hosted bundle would run.
# One image also means one rollback target — retagging an older SHA moves the
# API and the frontend back together.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci

# Vite inlines these at build time, so they must be present here rather than in
# the container's environment. Every one is public by nature — a Cognito domain,
# a public (no-secret) app client id, a Stripe *publishable* key. Build args are
# visible in `docker history`, so nothing secret may be added to this list; the
# Stripe secret key and the database DSN stay in SSM.
ARG VITE_API_BASE_URL=""
ARG VITE_COGNITO_DOMAIN=""
ARG VITE_COGNITO_CLIENT_ID=""
ARG VITE_COGNITO_REDIRECT_URI=""
ARG VITE_STRIPE_PUBLISHABLE_KEY=""
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL \
    VITE_COGNITO_DOMAIN=$VITE_COGNITO_DOMAIN \
    VITE_COGNITO_CLIENT_ID=$VITE_COGNITO_CLIENT_ID \
    VITE_COGNITO_REDIRECT_URI=$VITE_COGNITO_REDIRECT_URI \
    VITE_STRIPE_PUBLISHABLE_KEY=$VITE_STRIPE_PUBLISHABLE_KEY

COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/doorbust ./cmd

FROM alpine:3.20
# ca-certificates: doorbust makes outbound TLS calls to Neon and to Cognito's
# JWKS endpoint, and a bare Alpine image has no CA bundle.
RUN apk add --no-cache ca-certificates
COPY --from=build /out/doorbust /usr/local/bin/doorbust
COPY --from=web /web/dist /srv/web
# Read at startup by internal/web.Handler. A missing directory would degrade to
# a JSON 404 rather than failing, so this is set explicitly to make a broken
# frontend build visible as a 404 on / rather than a silently API-only image.
ENV WEB_DIST_DIR=/srv/web
# Run unprivileged: the app only needs to bind 8080 and make outbound calls.
RUN adduser -D -u 10001 doorbust
USER doorbust
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/doorbust"]
