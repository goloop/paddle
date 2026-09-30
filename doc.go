// Package paddle is a client for the Paddle Billing API and a verifier for
// its webhooks.
//
// It covers what a product that sells through Paddle needs from the server
// side: transactions with catalog or non-catalog prices, subscriptions
// (cancel, pause, resume, take back a scheduled change), refunds through
// adjustments, customers and customer portal sessions, and signed webhook
// notifications. The checkout itself runs in the browser (Paddle.js) on the
// transaction this package creates.
//
//	c := paddle.New(os.Getenv("PADDLE_API_KEY")) // a sandbox key talks to the sandbox
//	tx, err := c.CreateTransaction(ctx, &paddle.CreateTransactionRequest{
//	    Items: []paddle.TransactionItemRequest{{Price: &paddle.NonCatalogPrice{
//	        Description: "1000 credits",
//	        ProductID:   "pro_01...",
//	        UnitPrice:   paddle.Money{Amount: "1000", CurrencyCode: "USD"},
//	    }}},
//	    CustomData: map[string]any{"purchase": purchaseID},
//	})
//
// # Selling models
//
// One-time purchases at a catalog price or at an amount decided on the
// server (NonCatalogPrice); subscriptions with any billing cycle - monthly,
// yearly, several years - trials and seats; plan and seat changes with
// proration (ChangeSubscriptionItems); cancellation at the period end and
// taking it back (RemoveScheduledChange); invoices with payment terms for
// companies (manual collection with BillingDetails); full and partial
// refunds. Credit balances and plan allowances are not a Paddle concept: see
// goloop/ledger.
//
// # Webhooks
//
// A Webhook verifies the Paddle-Signature header against the raw body and
// decodes the notification. Deduplicate on Event.ID: Paddle delivers at
// least once, and a delivery that was not answered with a 2xx within five
// seconds comes again.
//
//	wh := paddle.NewWebhook(os.Getenv("PADDLE_WEBHOOK_SECRET"))
//	ev, err := wh.ParseRequest(r)
//	switch ev.Type {
//	case paddle.EventTransactionCompleted:
//	    tx, err := ev.Transaction()
//	    ...
//	}
//
// # Retries
//
// A request refused with 429 is retried after Retry-After on any method; a
// network error or a 5xx is retried on GET only. A POST that failed with a
// 5xx may have been applied, and the API takes no idempotency key, so
// repeating it could create a second transaction or a second refund. That
// decision is left to the caller, with the error in hand.
//
// # Errors
//
// An error response is an *APIError with Paddle's code, detail, the fields
// that failed validation and the request id for support. errors.Is(err,
// ErrNotFound) is true for a 404.
//
// The package depends only on the standard library.
package paddle
