# Demo script — the buyer's experience

About four minutes, shot in **one take with no edits** — nothing here needs a
server restart or a cut. This is the product side: what someone actually sees
and feels. No benchmarks, no SQL.

Narration is written to be spoken. Read it aloud once and change whatever
doesn't sound like you.

## Before you record

```bash
scripts/demo/seed.sh       # reset the catalogue
scripts/demo/server.sh     # start cleanly on localhost:8080
```

Use `server.sh` rather than `make server`. It stops whatever is already holding
the port first — `go run` leaves a child process behind that keeps answering
after you think you've stopped it, which is how you end up filming a server
running yesterday's settings.

**Logins** — two accounts, `demo-alice` and `demo-bob`, sharing one password.
The password is deliberately not written down in this repo; set one you know:

```bash
DEMO_PASSWORD='pick-something' scripts/demo/users.sh
```

Run it any time to reset both accounts — it is safe to re-run.

For the race in section 3, have two windows open side by side before you start
recording — Alice in one, Bob in the other. Sessions are per-tab, so two
ordinary windows keep separate logins; no incognito needed. If you'd rather keep
it simple, skip section 3 and run the happy path in one window.

**One thing to avoid on camera:** don't hard-refresh while you're on the orders
list or an order page. Those two URLs are currently shadowed by the API and a
reload shows raw JSON. Clicking through the app is fine — that's ordinary
navigation and it works. (Details in `CLAUDE.md`; `/checkout/...` reloads fine.)

---

## 1 · The catalogue (30s)

**Screen:** `localhost:8080`, signed out.

> This is a flash-sale storefront. Limited runs, everyone showing up at once.

**Do:** scroll slowly down the list.

> A few things going on here without me saying anything. These are live now.
> Down here are two that haven't started yet — you can look, you can't buy.
>
> The bars are stock. This one's healthy, this one's getting thin, and this one
> at the bottom is already gone. Sold-out items sink; nobody wants to scroll
> past things they can't have.

**Do:** hover on *Halo Wireless Earbuds* (sold out) and *Cirrus Down Jacket*
(upcoming) so both states are visible.

---

## 2 · Signing in (20s)

**Do:** click sign in. Real Cognito Hosted UI loads. Sign in as Alice.

> Sign-in is Amazon Cognito — the real hosted flow, with PKCE. I'm not storing
> anyone's password; I never see it.

**Do:** land back on the storefront, signed in.

---

## 3 · The race — what losing looks like (60s)

This is the one worth getting right.

**Screen:** both windows, Alice and Bob, on **Solstice Limited Print — 1 of 1**.

> One unit. Two people. Both about to click.

**Do:** count down out loud, click **Reserve my unit** in both, as close to
together as you can.

> One of them gets it.

**Do:** move to the losing window. Let the message sit on screen.

> And this is the bit I actually care about. *Gone. That run sold out.*
> And underneath — *nothing was reserved and you weren't charged.*
>
> That's the moment most sites get wrong. You either get a spinner that never
> resolves, or a generic red error, or worst of all you get a confirmation and
> then an apology email two days later saying they oversold.
>
> Here you get told immediately, in plain language, with the one reassurance
> that actually matters: your card wasn't touched.

**Do:** refresh the catalogue. Point at the stock.

> Zero. Not minus one. One unit, one order, one person who got it.

*If both clicks land too far apart and both succeed: `scripts/demo/seed.sh`
and retake.*

---

## 4 · The hold (40s)

**Screen:** Alice's window. Back to the catalogue.

**Do:** open **Aether Running Shoe**, click **Reserve my unit**.

> Now the normal path. Reserved — and there's a clock.

**Do:** let the countdown tick.

> That's a real hold. The unit's out of everyone else's reach while that runs,
> which is the honest way to do it — you're not competing with someone else
> mid-checkout, and nobody's holding it forever either.
>
> And that number comes from the order itself, not from the page. If we change
> how long a hold lasts, this updates on its own. Nothing to redeploy.

---

## 5 · Paying (60s)

**Do:** click through to checkout.

> Card details go straight to Stripe. They don't touch my server — I never
> receive a card number.

**Do:** `4242 4242 4242 4242`, any future expiry, any CVC. Submit.

**Do:** land on the order page. **Don't touch anything.** Let it sit.

> Watch this. I'm not refreshing.

**Do:** wait — it flips to completed in about six seconds.

> There it goes. *Payment received* became *confirming your unit*, and now it's
> done.
>
> Nothing I clicked did that. Stripe confirmed the payment on its own schedule
> and the app picked it up in the background. Which matters, because payments
> genuinely do take a few seconds — and the page tells you the truth while it
> happens instead of hanging or pretending.

### The two numbers (worth getting right)

**Do:** go back to the sale page for the shoe. Both numbers are on screen.

> And here's the part I like. Two numbers, and they moved at different moments.
>
> **Remaining** dropped the instant I reserved — before I'd paid a penny.
> That's the whole point of a hold: the unit left the shelf immediately so
> nobody else could take it while I was getting my card out.
>
> **Run size** only dropped now, when the money actually arrived. That's the
> count of units genuinely sold.
>
> So between reserving and paying, that unit belonged to nobody — it wasn't
> available to anyone else, and it wasn't sold yet either. Most of the hard
> problems in this project live in that gap.

| | Remaining | Run size |
| --- | --- | --- |
| Before | 250 | 250 |
| After reserve | **249** | 250 |
| After payment | 249 | **249** |

*Point at Run size for the payment beat, not Remaining. Remaining already moved
back at reserve time and will not change again — pointing at it here is the one
easy way to make a working demo look broken.*

## 6 · Orders (20s)

**Do:** click through to the orders list *(click — don't type the URL)*.

> Everything Alice has bought, with where each one stands. The completed one,
> and the one that expired sitting right there as expired — not quietly deleted
> so the page looks tidier.

---

## 7 · Close (25s)

> One thing I'd rather say than have you find.
>
> You can sign in here on localhost. On the deployed version you can browse but
> not log in — Cognito won't accept a login redirect that isn't HTTPS, and that
> box doesn't have a certificate yet. It's a domain purchase away, and I
> haven't made that call.

**Optional cutaway:** `http://18.191.71.225:8080` — the same catalogue on EC2.

> Same thing running on AWS. Readme's got the rest.

---

## Copy you'll see on screen

Handy for framing shots:

| Where | Text |
| --- | --- |
| Reserve button | *Reserve my unit* → *Reserving…* → *Sold out* |
| Lost the race | *Gone. That run sold out.* / *Nothing was reserved and you weren't charged.* |
| Order, held | *Held, not paid yet.* |
| Order, paying | *Payment received — confirming your unit…* |
| Order, done | *The vendor ships within 3 business days.* |
| Sale page labels | *Remaining*, *Run size*, *Total* |

## If something goes wrong

| Problem | Fix |
| --- | --- |
| Both buyers got the unit | Clicks too far apart. `scripts/demo/seed.sh`, retake. |
| Catalogue looks wrong | `scripts/demo/seed.sh` |
| Raw JSON on screen | You reloaded on `/orders` — navigate by clicking instead. |
| Settings not applying, or the site looks stale/down | A leftover server still holds :8080. `scripts/demo/server.sh` clears it and restarts. |
| Order stuck confirming | Stripe poll runs every 5s; it lands in about 6. Give it a beat. |
| Payment beat looked like nothing happened | You were watching *Remaining*. It moves at reserve; *Run size* is what moves at payment. |
| Sign-in page won't load | See the Cognito managed-login note in `CLAUDE.md`. |
