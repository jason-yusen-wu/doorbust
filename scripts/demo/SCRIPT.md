# Demo script — the buyer's experience

About four minutes. This is the product side: what someone actually sees and
feels. No benchmarks, no SQL — the things a performance number can't show you.

Narration is written to be spoken. Read it aloud once and change whatever
doesn't sound like you.

## Before you record

```bash
scripts/demo/seed.sh     # reset the catalogue
make server              # storefront + API on localhost:8080
```

Two browser windows side by side. Sign in as `demo-alice` in one and
`demo-bob` in the other — sessions are per-tab, so two ordinary windows keep
separate logins. No incognito needed.

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

## 5 · Paying (50s)

**Do:** click through to checkout.

> Card details go straight to Stripe. They don't touch my server — I never
> receive a card number.

**Do:** `4242 4242 4242 4242`, any future expiry, any CVC. Submit.

**Do:** land on the order page. **Don't touch anything.** Let it sit.

> Watch this. I'm not refreshing.

**Do:** wait for it to flip to completed.

> There it goes. *Payment received* became *confirming your unit*, and now it's
> done.
>
> Nothing I clicked did that. Stripe confirmed the payment on its own schedule
> and the app picked it up in the background. Which matters, because payments
> genuinely do take a few seconds sometimes — and the page just tells you the
> truth while it happens instead of pretending or hanging.

---

## 6 · The hold expires (40s) — optional but good

**Do:** restart with a short fuse:

```bash
pkill -f "exe/cmd"; pkill -f "go run ./cmd"
RESERVATION_TTL=45s RESERVATION_SWEEP_INTERVAL=10s make server
```

> Last thing about that hold. What if you reserve something and just… wander
> off?

**Do:** reserve something as Bob, don't pay, let the clock run out. Refresh the
catalogue.

> It's back. Somebody else can buy it now.
>
> That's on our clock, not the payment processor's. If Stripe went down
> completely and never got back to us, that stock would still return. Nothing
> gets stranded because a third party had a bad day.

---

## 7 · Orders (20s)

**Do:** click through to the orders list *(click — don't type the URL)*.

> Everything Alice has bought, with where each one stands. The completed one,
> and the one that expired sitting right there as expired — not quietly deleted
> so the page looks tidier.

---

## 8 · Close (25s)

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
| Settings not applying | Old server still on :8080 — `pkill -f "exe/cmd"` |
| Order stuck confirming | Stripe poll runs every 5s. Give it a beat. |
| Sign-in page won't load | See the Cognito managed-login note in `CLAUDE.md`. |
