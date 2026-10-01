# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-10-01

### Fixed
- The documentation and the example handled refunds and chargebacks from
  `adjustment.updated` only. A chargeback, and a refund approved at once,
  arrive only as `adjustment.created`; both events are now handled, with a
  table of which adjustment moves what.
- The client no longer follows redirects, so a request and its body never
  reach another host; a 3xx is an `*APIError`.
- Ids put into a request path are checked against an allowlist (letters,
  digits, `_`, `-`, at most 128 bytes) instead of a denylist that let `..`
  and escaped slashes through.
- `EnsureCustomer` works for an archived customer: `CustomerByEmail` looks up
  archived customers too, and `EnsureCustomer` brings one back to active.
  `CustomerByEmail` compares the address in the answer.
- The backoff no longer panics on a high attempt count, and
  `WithMaxRetries` is capped at `MaxRetries` (10).
- A request that cannot be built is returned at once instead of retried; a
  response beyond the size limit is an error instead of a truncated body.
- `WithHTTPClient(nil)` keeps the default client instead of panicking. An
  empty key and plain HTTP to a remote host are refused with `ErrInvalid`.

### Added
- `ListTransactions` and `ListAdjustments`, paged with `Page`, for a
  reconciliation job that finds what a lost webhook never delivered.
- `TransactionDetails.LineItems`, the line ids a partial refund names.
- `ParseAmount` and `Money.Int64`: amounts as integers, never through a
  float.
- `String`, `GoString` and `LogValue` on `Client` and `Webhook`, which never
  show the key or the secrets.
- `CustomerActive` and `CustomerArchived`.

## [0.1.0] - 2026-09-30

First release.

### Added
- First version: a Paddle Billing client for transactions (catalog and
  non-catalog prices), subscriptions (cancel, pause, resume, remove a
  scheduled change), adjustments and full refunds, customers with
  `EnsureCustomer`, and customer portal sessions.
- Webhook verification with secret rotation and a replay window, and typed
  decoding of transactions, subscriptions, adjustments and customers.
- Retries on 429 for every method and on 5xx for GET only; typed `*APIError`.
- `ChangeSubscriptionItems` for plan and seat changes with proration, and
  `BillingDetails` for invoices with manual collection.
- README and reference in English and Ukrainian: the problem the package
  solves, the pricing and selling models it supports, a real-world example.
