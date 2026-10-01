# paddle - reference

The full reference for the `paddle` package: the client, transactions,
subscriptions, adjustments, customers, webhooks, retries and errors.

Ukrainian version: **[DOC.UK.md](DOC.UK.md)**.

## Contents

- [Mental model](#mental-model)
- [Creating a client](#creating-a-client)
- [Transactions](#transactions)
- [Subscriptions](#subscriptions)
- [Refunds and adjustments](#refunds-and-adjustments)
- [Customers and the portal](#customers-and-the-portal)
- [Selling models](#selling-models)
- [Webhooks](#webhooks)
- [Reconciliation](#reconciliation)
- [Retries](#retries)
- [Errors](#errors)
- [Amounts and times](#amounts-and-times)
- [Scope](#scope)

## Mental model

Paddle is the merchant of record: it sells, charges, handles tax and refunds,
and tells your server what happened through signed webhooks. The server's
part is small - create a transaction for the checkout, act on the events,
occasionally cancel a subscription or ask for a refund - and that is what
this package covers.

The provider is the source of truth for money. Your database is the source
of truth for what the customer may use. Webhooks are the only road between
the two, so they are verified before anything else and deduplicated by
event id.

## Creating a client

```go
c := paddle.New(os.Getenv("PADDLE_API_KEY"))

c = paddle.New(apiKey,
	paddle.WithTimeout(10*time.Second),
	paddle.WithMaxRetries(3),
)
```

The host follows the key: a sandbox key (`pdl_sdbx_...`) talks to
`https://sandbox-api.paddle.com`, any other to `https://api.paddle.com`.
`WithSandbox(bool)` and `WithBaseURL(url)` override it; `Sandbox()` reports
where the client talks. Every request sends `Paddle-Version: 1`
(`WithVersion` changes it).

| Option | Default |
|---|---|
| `WithTimeout` | 30s (default HTTP client only) |
| `WithHTTPClient` | a new `http.Client`; nil keeps it |
| `WithMaxRetries` | 2, at most `MaxRetries` (10) |
| `WithHeader` | none |

What the client refuses, whatever the options:

- **Redirects.** The API has no reason to send one, and following it would
  send the request - with its body, and for a custom header its key - to
  another host. A 3xx comes back as an `*APIError`. A client passed to
  `WithHTTPClient` is copied for this, never changed.
- **Plain HTTP**, except to a loopback host (a test server): the key would
  travel in the clear. An empty key is refused too. Both fail with
  `ErrInvalid` before anything is sent.
- **An id that could change the path.** Ids may hold letters, digits, `_`
  and `-`, at most 128 bytes; anything else (`..`, an escaped slash, a
  space) fails with `ErrInvalid`.

The key never appears in output: `String`, `GoString` and `LogValue` show
the host only, so a client in a log line or an error message does not leak
it. A `Webhook` hides its secrets the same way.

## Transactions

A transaction is what the customer pays in the checkout. Create it on the
server, so the amount cannot be changed in the browser, and open the checkout
with its id.

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items: []paddle.TransactionItemRequest{
		{PriceID: "pri_monthly"}, // a catalog price, quantity 1
	},
	CustomerID: customer.ID, // optional
	CustomData: map[string]any{"purchase": purchaseID},
})
```

A **non-catalog price** charges an amount decided on the server under one
catalog product - a credit pack, a custom order - without a catalog price per
amount. `Description` and `ProductID` are required:

```go
{Price: &paddle.NonCatalogPrice{
	Description: "1000 credits",
	ProductID:   "pro_credits",
	UnitPrice:   paddle.Money{Amount: "1000", CurrencyCode: "USD"},
	TaxMode:     paddle.TaxModeAccountSetting, // the default
}}
```

`BillingCycle` on a non-catalog price makes it recurring.

The API takes no email on a transaction. When the address is known, find or
create the customer first (`EnsureCustomer`) and pass `CustomerID`; without
it the checkout asks for the email.

Put the id of your own purchase row in `CustomData`: it comes back on every
event about the transaction, so a webhook can be matched even if saving the
transaction id failed.

`GetTransaction(ctx, id)` reads a transaction, for reconciliation.
`Details.LineItems` holds its calculated lines; a partial refund names a line
by its `ID`.

## Subscriptions

```go
sub, err := c.GetSubscription(ctx, "sub_01...")

sub, err = c.CancelSubscription(ctx, id, paddle.EffectiveNextBillingPeriod) // the default
sub, err = c.CancelSubscription(ctx, id, paddle.EffectiveImmediately)

sub, err = c.RemoveScheduledChange(ctx, id) // the customer changed their mind

sub, err = c.PauseSubscription(ctx, id, &paddle.PauseRequest{ResumeAt: &date})
sub, err = c.ResumeSubscription(ctx, id, nil)
```

A cancellation at the end of the period leaves the subscription `active` with
a `ScheduledChange{Action: "cancel"}`, and `RemoveScheduledChange` takes it
back. An immediate cancellation is final: a canceled subscription cannot be
reinstated.

`Subscription.CurrentBillingPeriod` is the period the customer has paid for;
grant the plan's rights until its `EndsAt`. `ManagementURLs` holds the links
Paddle offers for updating the payment method and cancelling.

## Refunds and adjustments

```go
adj, err := c.RefundTransaction(ctx, "txn_01...", "Withdrawal within 14 days")
```

A refund needs a `completed` transaction. Most refunds start as
`AdjustmentPendingApproval`, and `adjustment.updated` reports `approved` or
`rejected` later; some are approved at once. A partial refund lists the lines
it covers, by the ids in `Details.LineItems`:

```go
tx, err := c.GetTransaction(ctx, "txn_01...")
adj, err := c.CreateAdjustment(ctx, &paddle.CreateAdjustmentRequest{
	Action:        paddle.ActionRefund,
	TransactionID: tx.ID,
	Reason:        "one of three items was not delivered",
	Type:          "partial",
	Items:         []paddle.AdjustmentItem{{ItemID: tx.Details.LineItems[2].ID, Type: "full"}},
})
```

Chargebacks arrive as adjustments too, created by Paddle: `ActionChargeback`
(the money went back), `ActionChargebackWarning` (a dispute was opened),
`ActionChargebackReverse` (the dispute was won, the money came back).

**Which event carries what.** `adjustment.updated` fires only for a refund
that waited for approval. A chargeback, and a refund approved at once, arrive
only as `adjustment.created`, already `approved`. Listen to both, act when
the status is `approved`, and key the ledger movement by the adjustment id -
so the second event of the pair is a no-op:

| Adjustment | Take back what the money bought | Give it back |
|---|---|---|
| `ActionRefund`, approved | yes | |
| `ActionChargeback`, approved | yes | |
| `ActionChargebackReverse`, approved | | yes, as much as the chargeback took |
| `ActionChargebackWarning` | no: nothing moved yet | |
| status `reversed` | | yes: the adjustment was undone |

## Customers and the portal

```go
cu, err := c.EnsureCustomer(ctx, "ann@example.com", "Ann") // find or create
cu, err = c.CustomerByEmail(ctx, "ann@example.com")        // ErrNotFound if none
cu, err = c.GetCustomer(ctx, "ctm_01...")

ps, err := c.CreatePortalSession(ctx, cu.ID, sub.ID)
// redirect to ps.URLs.General.Overview or ps.URLs.Subscriptions[0].CancelSubscription
```

`CreateCustomer` fails with code `customer_already_exists` when the address
belongs to a customer; `EnsureCustomer` handles that, two requests racing to
create the same customer, and a customer that was archived (it is brought
back to active). `CustomerByEmail` finds active and archived customers and
compares the address in the answer, so it never returns someone else.

**Call both only with an address the user has proved they own.** The
customer they return opens portal sessions with the invoices, addresses and
subscriptions of whoever registered the address at Paddle; an unverified
sign-up with someone else's address would hand those over. For the same
reason, pass to `CreatePortalSession` only a customer id your database ties
to the signed-in account.

Portal links are temporary: create a session when the customer asks and
never store the links.

## Selling models

Paddle's catalog has **products** (what you sell) and **prices** (how much,
how often). A transaction bills prices; a subscription is what a transaction
with a recurring price turns into once it is paid. Every model below is a
combination of those two.

### One-time purchase at a fixed price

A licence, a fixed pack, an add-on. Create the price in the catalog and bill
it by id:

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items:      []paddle.TransactionItemRequest{{PriceID: "pri_maker_licence"}},
	CustomData: map[string]any{"purchase": purchaseID},
})
```

On `transaction.completed`, grant the right (a licence row with an expiry for
updates) in your entitlements table.

### One-time purchase, amount from the server

Credit packs with a custom amount, an order priced by a formula, a top-up.
One catalog product, a non-catalog price per transaction:

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items: []paddle.TransactionItemRequest{{Price: &paddle.NonCatalogPrice{
		Description: fmt.Sprintf("%d credits", pack.Credits),
		ProductID:   "pro_credits",
		UnitPrice:   paddle.Money{Amount: strconv.FormatInt(pack.Cents, 10), CurrencyCode: "EUR"},
	}}},
	CustomData: map[string]any{"purchase": purchaseID},
})
```

When the webhook arrives, check `tx.Details.Totals.Subtotal` and the currency
against your purchase row before crediting: a transaction can be changed at
Paddle after you created it.

### Subscriptions and terms

A recurring price has a `billing_cycle`: `month` x `1`, `year` x `1`. Create
one price per term under the same product, and sell the term the customer
picks:

| Term | Price | Per month |
|---|---|---|
| monthly | `pri_pro_month`, $15, month x 1 | $15 |
| yearly | `pri_pro_year`, $60, year x 1 | $5 |
| two years | `pri_pro_2y`, $100, year x 2 | $4.17 |

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items:      []paddle.TransactionItemRequest{{PriceID: "pri_pro_year"}},
	CustomerID: cu.ID,
	CustomData: map[string]any{"account": accountID},
})
```

Once paid, the transaction creates a subscription and `subscription.created`
/ `subscription.activated` arrive. Grant the plan's rights until
`CurrentBillingPeriod.EndsAt`, and move that date on every
`subscription.updated`. A frequency above one is accepted by the schema;
check a multi-year cycle in the sandbox before you advertise it.

A recurring amount decided on the server - a negotiated plan - is a
`NonCatalogPrice` with `BillingCycle` set.

### Trials

Set `trial_period` on the catalog price (or `TrialPeriod` on a non-catalog
one). The subscription starts as `trialing` and bills when the trial ends; a
cancellation during the trial costs nothing.

### Seats

A per-seat price billed with `Quantity`:

```go
// Ten seats at the start.
Items: []paddle.TransactionItemRequest{{PriceID: "pri_team_seat_month", Quantity: 10}}

// Two more members later: bill the difference now.
sub, err := c.ChangeSubscriptionItems(ctx, sub.ID,
	[]paddle.SubscriptionItemRequest{{PriceID: "pri_team_seat_month", Quantity: 12}},
	paddle.ProratedImmediately)
```

The items passed are the whole new set.

### Plan changes

Monthly to yearly, Personal to Team, an upgrade or a downgrade - replace the
price:

```go
sub, err := c.ChangeSubscriptionItems(ctx, sub.ID,
	[]paddle.SubscriptionItemRequest{{PriceID: "pri_pro_year"}},
	paddle.ProratedImmediately)
```

| Proration mode | Effect |
|---|---|
| `ProratedImmediately` | charge or credit the difference now (upgrades) |
| `ProratedNextBillingPeriod` | put the difference on the next bill |
| `FullImmediately` | charge the new price in full now |
| `FullNextBillingPeriod` | switch at the next renewal, no proration (downgrades) |
| `DoNotBill` | change without billing (support gestures) |

### Cancelling, pausing, changing one's mind

```go
sub, err := c.CancelSubscription(ctx, id, paddle.EffectiveNextBillingPeriod)
sub, err = c.RemoveScheduledChange(ctx, id) // "I changed my mind"
sub, err = c.PauseSubscription(ctx, id, &paddle.PauseRequest{ResumeAt: &resume})
```

A cancellation at the period end keeps the rights until `EndsAt`; the
subscription turns `canceled` then, with a `subscription.canceled` event. For
a customer-facing button, `CreatePortalSession` gives Paddle's own pages for
cancelling and updating the card.

### Invoices for companies

A company that pays by bank transfer on terms:

```go
tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items:          []paddle.TransactionItemRequest{{PriceID: "pri_team_seat_year", Quantity: 25}},
	CustomerID:     cu.ID, // the customer needs a name
	AddressID:      addressID,
	BusinessID:     businessID,
	CollectionMode: "manual",
	BillingDetails: &paddle.BillingDetails{
		EnableCheckout:      true, // they may also pay by card
		PaymentTerms:        paddle.Duration{Interval: "day", Frequency: 30},
		PurchaseOrderNumber: "PO-2026-114",
	},
})
```

### Refunds, withdrawals and chargebacks

```go
adj, err := c.RefundTransaction(ctx, tx.ID, "Withdrawal within 14 days")
```

Take back what the money bought when an adjustment is `approved`, keyed by
the adjustment id, from `adjustment.created` and `adjustment.updated` both:
a chargeback and a refund approved at once never send the second. See
[Refunds and adjustments](#refunds-and-adjustments) for the table.

### Credits, allowances and usage

Paddle does not keep a balance of credits and does not meter usage. Sell
credit packs as one-time purchases (above), keep the balance and the plan
allowances in [goloop/ledger](https://github.com/goloop/ledger), and grant a
subscription's monthly allowance from its webhook. The combination covers
pay-per-action, priced items, plans with an allowance and overage.

## Webhooks

```go
wh := paddle.NewWebhook(secret)
ev, err := wh.ParseRequest(r)
```

Verification:

- The `Paddle-Signature` header is `ts=<unix>;h1=<hex>`, where `h1` is the
  HMAC-SHA256 of `<ts>:<raw body>` under the destination's secret key.
- The body must be the bytes as received. A body decoded and encoded again
  does not verify.
- While Paddle rotates a secret it sends several `h1` values; any match is
  enough. `WithPreviousSecret` accepts an older secret on your side.
- The timestamp must be within `DefaultTolerance` (5 seconds, the value
  Paddle documents) of the local clock, or the delivery fails with
  `ErrSignatureExpired`. A clock more than 5 seconds off rejects **every**
  delivery, payments included, until it is fixed. Keep the server clock
  synchronized, count `ErrSignatureExpired` in your metrics, and where time
  synchronization is not guaranteed use `WithTolerance(30*time.Second)`: a
  replay is already stopped by the event ids you store and by the ledger's
  keys, so the wider window costs little.
- An empty secret verifies nothing, so a misconfigured service rejects every
  delivery instead of accepting forgeries.

`ErrSignatureExpired` matches `ErrSignature`, so one check covers both.

Handling:

- Deduplicate on `ev.ID`: Paddle delivers at least once.
- Answer 2xx within five seconds; a slow answer counts as a failure and the
  delivery comes again. When processing can take longer, store the event,
  answer, and process it from a queue.
- Mark an event done only after it was applied. Marking it first loses it
  when applying fails: the retry would find it done.
- Paddle retries a failed delivery for three days, then gives up: see
  [Reconciliation](#reconciliation).
- Events can arrive out of order. Use `ev.OccurredAt` or the entity's own
  `UpdatedAt` to ignore an event older than the state already applied.

Decoding: `ev.Transaction()`, `ev.Subscription()`, `ev.Adjustment()`,
`ev.Customer()`, or `ev.Decode(&v)` for anything else. The `Event*` constants
name the common types; any other type still parses.

`Sign(secret, body, at)` builds a valid header, for tests of your handler.

## Reconciliation

A webhook can be lost for good: a bug, an outage longer than Paddle's three
days of retries, a secret rotated wrong. A payment whose webhook never
arrived is a customer who paid and got nothing. A periodic job closes the gap:

```go
from := lastRun.Add(-time.Hour) // overlap: applying twice is a no-op
req := &paddle.ListTransactionsRequest{
	Status:      []string{paddle.TransactionCompleted},
	UpdatedFrom: from,
	OrderBy:     "updated_at[ASC]",
}
for {
	page, err := c.ListTransactions(ctx, req)
	if err != nil {
		return err
	}
	for _, tx := range page.Items {
		if err := purchases.Paid(ctx, &tx); err != nil { // the webhook's own code
			return err
		}
	}
	if !page.HasMore {
		break
	}
	req.After = page.After
}
```

`ListAdjustments` does the same for refunds and chargebacks (`Action`,
`Status: approved`). Because every ledger movement is keyed by the provider's
id, a transaction the webhook already applied is a no-op here.

`ListTransactions` pages up to 30 items, `ListAdjustments` up to 50.

## Retries

| Failure | GET | POST, PATCH |
|---|---|---|
| 429 | retried, after `Retry-After` | retried, after `Retry-After` |
| 5xx | retried with backoff | returned |
| network error | retried with backoff | returned |
| other 4xx | returned | returned |

A 429 means the request was refused before it ran, so repeating it is safe
for any method. A POST that failed with a 5xx or a broken connection may have
been applied, and the API takes no idempotency key: repeating it could create
a second transaction or a second refund. The error is returned and the
caller decides - usually by reading the state back (`GetTransaction`) before
trying again.

`Retry-After` is capped at 60 seconds; the backoff is 0.5s, 1s, 2s ... with
jitter, at most 8s. A cancelled context stops the wait. A request that cannot
be built - a malformed base URL - fails at once, without retries.

## Errors

An error response is an `*APIError`:

```go
var ae *paddle.APIError
if errors.As(err, &ae) {
	log.Printf("%d %s: %s (request %s)", ae.Status, ae.Code, ae.Detail, ae.RequestID)
	for _, f := range ae.Errors {
		log.Printf("  %s: %s", f.Field, f.Message)
	}
}
if errors.Is(err, paddle.ErrNotFound) { ... } // any 404
```

`ErrInvalid` reports a request refused before it was sent: a missing field,
an id outside the allowlist, an empty key, a base URL over plain HTTP.

## Amounts and times

Amounts are strings in the lowest denomination of the currency, exactly as
Paddle sends them: `"1000"` is $10.00. Convert with `ParseAmount` (or
`Money.Int64`), which accepts digits only - no decimal point, no exponent -
so an amount never passes through a float on its way to a balance. Times are
`time.Time`, optional ones `*time.Time`.

## Scope

Server-side calls a product needs, not the whole API: no catalog management,
reports, discounts or address APIs. The shared `do` path takes any endpoint,
so a missing call is a small addition, not a new client.

The package depends only on the standard library.
