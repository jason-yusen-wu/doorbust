# Filming a demo

Everything here is reset-able, so you can retake any shot without cleaning up by
hand.

## Why the demo runs on localhost

**Film on `http://localhost:8080`.** That is the production topology exactly:
one Go binary serving the compiled React bundle from a single origin. The only
thing production has that this does not is a TLS certificate.

That certificate is also the reason you cannot film on the deployed box. Cognito
refuses to register a callback URL that is not HTTPS, with one exemption for
loopback, so `http://localhost:8080/auth/callback` is registered and the
deployed box's address never can be. **Sign-in works locally and nowhere else.**
The deployed box still browses the catalogue, which makes it a good
five-second "and it's really running on AWS" shot, not a place to film the flow.

## Setup

```bash
scripts/demo/build-frontend.sh   # builds the bundle for :8080 (leaves web/.env alone)
scripts/demo/seed.sh             # loads the demo catalogue
make server                      # API + storefront on :8080
```

Open <http://localhost:8080>.

**Reset between takes:** `scripts/demo/seed.sh`. It restores all eight products
and clears their orders. It only touches products it created, and it does not
truncate anything.

## Accounts

Two buyers, so the race reads as two people rather than one person clicking
twice. Create them, and set a password you choose:

```bash
DEMO_PASSWORD='pick-something' scripts/demo/users.sh
```

That creates `demo-alice` and `demo-bob`, both confirmed with a permanent
password so Cognito does not interrupt you with a password-change prompt
mid-take. Omit `DEMO_PASSWORD` and it generates one and prints it once.

The password is deliberately not committed anywhere in this repo — these are
real accounts in a real Cognito pool, and a working credential in a public
repository is a working credential no matter how limited the account.

Sign in through the Cognito Hosted UI — the real flow, PKCE and all.

**You can sign in as both at once in two ordinary windows.** The session is kept
in `sessionStorage`, which is per-tab, so the two windows do not fight over one
login. No incognito needed.

## The catalogue

Arranged so every state the storefront can render is on screen at once:

| Product | Stock | What it shows |
| --- | --- | --- |
| Aether Running Shoe | 250 | Healthy stock — the ordinary path |
| Lumen 27" 4K Monitor | 40 | — |
| Nomad Weekender Duffel | 12 | Stock bar starting to look thin |
| Vertex Mechanical Keyboard | 3 | Nearly gone |
| **Solstice Limited Print** | **1** | **The race** |
| Halo Wireless Earbuds | 0 | Sold out — sorts last, renders unavailable |
| Cirrus Down Jacket | 100 | Upcoming — listed separately, not yet reservable |
| Terra Cast Iron Set | 60 | Upcoming, further out |

## The script

Shot-by-shot narration is in [SCRIPT.md](SCRIPT.md) — a four-minute run through
the buyer's experience.

## Checks before you record

```bash
curl -s localhost:8080/health/ready          # {"status":"ok",...}
curl -s localhost:8080/products | head       # eight products
```

- Stripe keys are **test mode** (`sk_test_` / `pk_test_`) — verify before
  showing any card entry on camera.
- Don't hard-refresh on `/orders` or `/orders/{id}`: the API shadows those two
  paths on a single origin and a reload returns JSON. Clicking through the app
  is fine. See `CLAUDE.md`.
- The dev token-paste login is compiled out of this build, so `/signin` shows
  the real Cognito button only.
- Hide anything showing your `.env`, and do not film the terminal you ran
  `aws` commands in.
