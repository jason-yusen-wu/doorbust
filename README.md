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

## CI/CD
Push to `main` and GitHub Actions does the rest: vet/build/sqlc-drift checks
and `go test -race` against a throwaway Postgres, then a SHA-tagged image to
ECR, `goose up` against Neon, and a health-gated rollout to EC2 driven by SSM
Run Command. No SSH key in GitHub, no inbound ports opened, and no long-lived
AWS keys — the runner authenticates via GitHub OIDC. See `.github/workflows/ci-cd.yml`.

## Deploying (benchmarking box)
`infra/` (Terraform) provisions a single EC2 instance for load-testing — no
load balancer, no ECS. Terraform is always applied by hand from a workstation,
since state is local. `scripts/deploy.sh` builds and ships the image over SSH
as a manual escape hatch for when CI is broken or you want to try an
uncommitted build; the pipeline above is the normal path. See the Commands
section of `CLAUDE.md` for the full flow.

## System design
[Link to whiteboard](https://www.tldraw.com/f/UTNkBEuEvJF9yf0wjVOPS?d=v0.0.1660.989.page)
