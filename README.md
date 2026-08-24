# Doorbust 🛒🏃‍♂️

A modern, simple, and robust Go eCommerce backend with oversell-free, idempotent inventory guarantees under extreme contention. Proven with real load-testing benchmarks, and easily configuration for real-world production.

## Tech stack
* **Language**: Go 1.26
* **Framework**: chi (v5)
* **Database**: PostgreSQL 18 (+ pgx & sqlc)
* **Cache**: Redis
* **Authentication**: Amazon Cognito (ID token, verified as a resource server — see `internal/auth/`)

## Quick start
1. `cp .env.example .env` and fill in a Postgres connection string (we run on [Neon](https://neon.tech)) and your Cognito user pool's issuer URL + app client ID.
2. `make server`

## Deploying (benchmarking box)
`infra/` (Terraform) provisions a single, non-always-on EC2 instance for
load-testing — no load balancer, started/stopped per benchmarking session.
`scripts/deploy.sh` builds the `Dockerfile` image and ships it to the
instance. See the Commands section of `CLAUDE.md` for the full flow.

## System design
[Link to whiteboard](https://www.tldraw.com/f/UTNkBEuEvJF9yf0wjVOPS?d=v0.0.1660.989.page)
