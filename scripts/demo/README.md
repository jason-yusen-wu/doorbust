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

## Suggested running order

### 1. The problem (20s)

Land on the storefront. Point at **Solstice Limited Print — 1 of 1**. One unit,
two buyers about to click at the same time.

### 2. The race (45s) — the centrepiece

Two windows side by side, Alice and Bob, both on the Solstice page. Count down,
both click **Reserve**.

One gets an order. The other gets *"Gone. That run sold out."* Refresh: stock is
**0**, not −1, and exactly one order exists.

This is the whole thesis in one shot. Everything else is supporting material.

### 3. Why it works (40s)

Cut to `internal/adapters/postgresql/sqlc/queries.sql`, the `ReserveStock`
query. The point to make out loud: **the check is inside the write**, so there
is no gap between reading stock and acting on it. Zero rows affected is the
answer — it means sold out.

Worth adding: the same one-statement idea also makes Stripe's at-least-once
webhook delivery safe, with no distributed lock anywhere in the system.

### 4. The purchase flow (60s)

Reserve the **Aether Running Shoe**. Show:

- the **countdown** — it is read from the order's own `expires_at`, not a
  hardcoded 15:00
- **checkout** → Stripe test card `4242 4242 4242 4242`, any future expiry, any CVC
- the order flipping to **completed** on its own a few seconds later

That last beat is the interesting one: nothing in the request completed the
order. Stripe confirmed the payment, a background worker picked the event up,
and *that* completed it. Payment is split in two halves on purpose — calling
Stripe while holding the inventory lock would make one buyer's slow card
everyone else's outage.

### 5. Reservations expire (40s, optional)

Restart the server with a short fuse:

```bash
RESERVATION_TTL=45s RESERVATION_SWEEP_INTERVAL=10s make server
```

Reserve something and do not pay. Watch the countdown run out, then refresh the
catalogue — the unit is back on sale, about 25 seconds after you reserved it.
This is our timeout on our clock, so stock returns even if the payment processor
never answers.

The server log shows the moment it happens, which is a good cutaway:

```
msg="released expired reservations" count=1
```

**Restarting with different settings:** `make server` runs `go run`, which
leaves a compiled child process holding port 8080 if it is not stopped cleanly.
If the new settings do not seem to apply, that is why — the old server is still
answering. Stop it properly first:

```bash
pkill -f "exe/cmd"; pkill -f "go run ./cmd"
lsof -nP -iTCP:8080 -sTCP:LISTEN     # should print nothing
```

### 6. The measurements (60s) — the differentiator

Most projects claim they are fast. Show it:

```bash
make bench-db
make bench-sweep
```

The number to put on screen is the contrast at a fixed 2,000 reservations/second:

| version | median response |
| --- | --- |
| original — six round trips, lock held across two | **2.17 seconds** |
| rewritten — one SQL statement, lock held across none | **376 µs** |

Say what the lever was: not a cleverer lock, just holding the lock across fewer
network round trips. And that it removed the reason to add Redis at all.

Committed results are in [`bench/results/`](../../bench/results/) if you want to
show that the numbers are checked in next to the code.

### 7. It's really deployed (20s)

<http://18.191.71.225:8080> — same catalogue, running on EC2, deployed by
GitHub Actions with no SSH key and no long-lived AWS credentials.

*(Reachable only from the IP in the security group — yours. If it does not load,
your IP changed: update `allowed_cidr` in `infra/terraform.tfvars` and re-apply.)*

### 8. What is not finished (20s)

Worth including — it reads as judgement, not weakness:

- You cannot sign in on the deployed site, because there is no TLS certificate,
  and Cognito will not accept a plain-HTTP callback.
- The same missing certificate is why Stripe events are polled rather than
  pushed — which turns out to recover better from outages anyway.

## Checks before you record

```bash
curl -s localhost:8080/health/ready          # {"status":"ok",...}
curl -s localhost:8080/products | head       # eight products
```

- Stripe keys are **test mode** (`sk_test_` / `pk_test_`) — verify before
  showing any card entry on camera.
- The dev token-paste login is compiled out of this build, so `/signin` shows
  the real Cognito button only.
- Hide anything showing your `.env`, and do not film the terminal you ran
  `aws` commands in.
