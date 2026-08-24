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
# Run unprivileged: the app only needs to bind 8080 and make outbound calls.
RUN adduser -D -u 10001 doorbust
USER doorbust
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/doorbust"]
