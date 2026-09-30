[![Go Reference](https://img.shields.io/badge/godoc-reference-blue)](https://pkg.go.dev/github.com/goloop/paddle) [![License](https://img.shields.io/badge/license-MIT-brightgreen)](https://github.com/goloop/paddle/blob/master/LICENSE) [![Stay with Ukraine](https://img.shields.io/static/v1?label=Stay%20with&message=Ukraine%20♥&color=ffD700&labelColor=0057B8&style=flat)](https://u24.gov.ua/)

# paddle

`paddle` is a Go client for the Paddle Billing API and a verifier for its
webhooks: the server side of selling through Paddle as merchant of record -
one-time purchases, custom amounts, subscriptions with any term, seats,
plan changes, invoices, refunds and signed notifications. Standard library
only.

Ukrainian version: **[README.UK.md](README.UK.md)**.

## The problem it solves

Paddle sells, charges, handles VAT and sales tax, and tells your server what
happened. The server's part is small, but each piece has a trap:

- **The amount must come from the server.** A price set in the browser can
  be changed in the browser. Transactions are created here, with catalog or
  non-catalog prices, and the checkout only opens them.
- **Webhooks must be verified on the raw bytes, in time.** A body decoded
  and encoded again no longer verifies; an old delivery replayed later must
  not; a secret being rotated must keep working.
- **A failed POST may have succeeded.** Retrying a transaction or a refund
  after a 5xx can create a second one; the API takes no idempotency key.
  This client retries only what cannot have run.
- **Sandbox keys must not reach production.** The host is chosen from the
  key itself.

The package does those four things right and keeps the rest a thin, typed
mapping of the API.

## Features

- Transactions: catalog prices, non-catalog prices for amounts decided on
  the server, custom data that comes back on every event, invoices with
  manual collection.
- Subscriptions: get, cancel at the end of the period or at once, take back
  a scheduled change, pause, resume, change plan or seats with proration.
- Refunds (full or partial) and chargebacks through adjustments.
- Customers (`EnsureCustomer` survives a race) and customer portal sessions.
- Webhooks: signature check with rotation and a replay window, typed events.
- Typed `*APIError`; retries that cannot double-charge.
- Zero dependencies.

## Installation

```bash
go get github.com/goloop/paddle
```

## Quick start

```go
c := paddle.New(os.Getenv("PADDLE_API_KEY")) // a sandbox key talks to the sandbox

tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
	Items: []paddle.TransactionItemRequest{{Price: &paddle.NonCatalogPrice{
		Description: "1000 credits",
		ProductID:   "pro_credits",
		UnitPrice:   paddle.Money{Amount: "1000", CurrencyCode: "USD"},
	}}},
	CustomData: map[string]any{"purchase": purchaseID},
})
// open the checkout in the browser: Paddle.Checkout.open({transactionId: tx.ID})
```

## Selling models

| Model | Example | How |
|---|---|---|
| **One-time, fixed price** | a licence, a fixed pack | catalog `PriceID` |
| **One-time, amount from the server** | credit packs, custom orders | `NonCatalogPrice` under one product |
| **Subscription** | $15 a month, $60 a year | catalog recurring price (`billing_cycle`) |
| **Longer terms** | $100 for two years | `billing_cycle` `year` x `2` - verify in the sandbox first |
| **Recurring amount from the server** | a negotiated plan | `NonCatalogPrice` with `BillingCycle` |
| **Trials** | 14 days free | `trial_period` on the price |
| **Seats** | $8 per member a month | `Quantity`; `ChangeSubscriptionItems` to add or remove |
| **Plan changes** | monthly to yearly, Personal to Team | `ChangeSubscriptionItems` with a proration mode |
| **Cancel, pause, change of mind** | cancel at the period end | `CancelSubscription`, `RemoveScheduledChange`, `PauseSubscription` |
| **Invoices for companies** | net 30, purchase order | `CollectionMode: "manual"` + `BillingDetails` |
| **Refunds and chargebacks** | 14-day withdrawal | `RefundTransaction`, adjustment webhooks |

Credits, allowances and priced items paid from a wallet are not a Paddle
concept: sell packs here and keep the balance in
[goloop/ledger](https://github.com/goloop/ledger). The reference shows every
model in code: [Selling models](DOC.md#selling-models).

## A real-world example

```go
wh := paddle.NewWebhook(os.Getenv("PADDLE_WEBHOOK_SECRET"))

http.HandleFunc("POST /webhooks/paddle", func(w http.ResponseWriter, r *http.Request) {
	ev, err := wh.ParseRequest(r)
	if err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	if seen, _ := events.Seen(r.Context(), ev.ID); seen { // your table
		w.WriteHeader(http.StatusOK)
		return
	}
	switch ev.Type {
	case paddle.EventTransactionCompleted:
		tx, _ := ev.Transaction()
		err = purchases.Paid(r.Context(), tx.CustomData["purchase"], tx.ID)
	case paddle.EventSubscriptionUpdated, paddle.EventSubscriptionCanceled:
		sub, _ := ev.Subscription()
		err = entitlements.Sync(r.Context(), sub, ev.OccurredAt)
	case paddle.EventAdjustmentUpdated:
		adj, _ := ev.Adjustment()
		err = purchases.Adjusted(r.Context(), adj)
	}
	if err != nil {
		http.Error(w, "retry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
})
```

## The API at a glance

| Call | Endpoint |
|---|---|
| `CreateTransaction`, `GetTransaction` | `/transactions` |
| `GetSubscription`, `CancelSubscription`, `PauseSubscription`, `ResumeSubscription` | `/subscriptions/{id}[/cancel,/pause,/resume]` |
| `ChangeSubscriptionItems`, `RemoveScheduledChange` | `PATCH /subscriptions/{id}` |
| `CreateAdjustment`, `RefundTransaction` | `/adjustments` |
| `CreateCustomer`, `GetCustomer`, `CustomerByEmail`, `EnsureCustomer` | `/customers` |
| `CreatePortalSession` | `/customers/{id}/portal-sessions` |
| `NewWebhook`, `Webhook.ParseRequest`, `Sign` | notifications |

## Documentation

- Full reference and selling models: [DOC.md](DOC.md) · [DOC.UK.md](DOC.UK.md)
- Package API: [pkg.go.dev/github.com/goloop/paddle](https://pkg.go.dev/github.com/goloop/paddle)
- Changes between versions: [CHANGELOG.md](CHANGELOG.md)
- Credit wallets on top: [goloop/ledger](https://github.com/goloop/ledger)

## Contributing

Contributions are welcome. Please run `go test -race ./...`, `go vet ./...`
and `gofmt -l .` before submitting a pull request.

## License

`paddle` is released under the MIT License. See [LICENSE](LICENSE).
