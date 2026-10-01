package paddle

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultTolerance is how far the signature's timestamp may be from the
// local clock, the value Paddle documents.
const DefaultTolerance = 5 * time.Second

// DefaultMaxBody bounds the webhook body ParseRequest reads.
const DefaultMaxBody = 1 << 20

var (
	// ErrSignature reports a webhook whose Paddle-Signature header is
	// missing, malformed or does not match the body.
	ErrSignature = errors.New("paddle: bad webhook signature")

	// ErrSignatureExpired reports a correctly signed webhook whose timestamp
	// is outside the tolerance: a replay of a captured delivery, or a clock
	// that drifted. It matches ErrSignature too.
	ErrSignatureExpired = fmt.Errorf("%w: timestamp outside tolerance", ErrSignature)
)

// Event types this package names. Any other type still parses; compare
// Event.Type with the string.
const (
	EventTransactionCreated       = "transaction.created"
	EventTransactionReady         = "transaction.ready"
	EventTransactionBilled        = "transaction.billed"
	EventTransactionPaid          = "transaction.paid"
	EventTransactionCompleted     = "transaction.completed"
	EventTransactionPaymentFailed = "transaction.payment_failed"
	EventTransactionCanceled      = "transaction.canceled"
	EventTransactionPastDue       = "transaction.past_due"
	EventTransactionUpdated       = "transaction.updated"

	EventSubscriptionCreated   = "subscription.created"
	EventSubscriptionActivated = "subscription.activated"
	EventSubscriptionUpdated   = "subscription.updated"
	EventSubscriptionCanceled  = "subscription.canceled"
	EventSubscriptionPastDue   = "subscription.past_due"
	EventSubscriptionPaused    = "subscription.paused"
	EventSubscriptionResumed   = "subscription.resumed"
	EventSubscriptionTrialing  = "subscription.trialing"

	EventAdjustmentCreated = "adjustment.created"
	EventAdjustmentUpdated = "adjustment.updated"

	EventCustomerCreated = "customer.created"
	EventCustomerUpdated = "customer.updated"
)

// Event is one verified webhook notification.
type Event struct {
	// ID identifies the event; deduplicate on it; a delivery can arrive more
	// than once. NotificationID identifies this delivery of it.
	ID             string    `json:"event_id"`
	Type           string    `json:"event_type"`
	OccurredAt     time.Time `json:"occurred_at"`
	NotificationID string    `json:"notification_id"`

	// Data is the entity the event is about, as sent.
	Data json.RawMessage `json:"data"`
}

// Decode unmarshals the event's entity into v.
func (e *Event) Decode(v any) error {
	if err := json.Unmarshal(e.Data, v); err != nil {
		return fmt.Errorf("paddle: decode %s: %w", e.Type, err)
	}
	return nil
}

// Transaction decodes the entity of a transaction.* event.
func (e *Event) Transaction() (*Transaction, error) {
	var t Transaction
	return &t, e.Decode(&t)
}

// Subscription decodes the entity of a subscription.* event.
func (e *Event) Subscription() (*Subscription, error) {
	var s Subscription
	return &s, e.Decode(&s)
}

// Adjustment decodes the entity of an adjustment.* event.
func (e *Event) Adjustment() (*Adjustment, error) {
	var a Adjustment
	return &a, e.Decode(&a)
}

// Customer decodes the entity of a customer.* event.
func (e *Event) Customer() (*Customer, error) {
	var c Customer
	return &c, e.Decode(&c)
}

// Webhook verifies and parses notifications sent to one destination.
//
// A Webhook is safe for concurrent use.
type Webhook struct {
	secrets   [][]byte
	tolerance time.Duration
	maxBody   int64
	now       func() time.Time
}

// WebhookOption configures a Webhook in NewWebhook.
type WebhookOption func(*Webhook)

// WithPreviousSecret also accepts signatures made with an older secret, for
// the time a rotated secret is still in use somewhere.
func WithPreviousSecret(secret string) WebhookOption {
	return func(w *Webhook) {
		if secret != "" {
			w.secrets = append(w.secrets, []byte(secret))
		}
	}
}

// WithTolerance sets how far the signature's timestamp may be from the
// local clock. The default is DefaultTolerance.
func WithTolerance(d time.Duration) WebhookOption {
	return func(w *Webhook) { w.tolerance = d }
}

// WithMaxBody sets how many bytes ParseRequest reads. The default is
// DefaultMaxBody.
func WithMaxBody(n int64) WebhookOption {
	return func(w *Webhook) { w.maxBody = n }
}

// WithWebhookClock replaces time.Now, for tests.
func WithWebhookClock(now func() time.Time) WebhookOption {
	return func(w *Webhook) { w.now = now }
}

// NewWebhook returns a Webhook for the destination's secret key. An empty
// secret verifies nothing: every delivery fails with ErrSignature, which is
// the safe way for a misconfigured service to fail.
func NewWebhook(secret string, opts ...WebhookOption) *Webhook {
	w := &Webhook{tolerance: DefaultTolerance, maxBody: DefaultMaxBody, now: time.Now}
	if secret != "" {
		w.secrets = append(w.secrets, []byte(secret))
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// String describes the webhook without its secrets.
func (w *Webhook) String() string {
	return fmt.Sprintf("paddle.Webhook(%d secrets, tolerance %s)", len(w.secrets), w.tolerance)
}

// GoString is String, for %#v.
func (w *Webhook) GoString() string { return w.String() }

// LogValue implements slog.LogValuer, without the secrets.
func (w *Webhook) LogValue() slog.Value { return slog.StringValue(w.String()) }

// Verify checks a Paddle-Signature header against the raw body.
//
// The header is "ts=<unix seconds>;h1=<hex>", where h1 is the HMAC-SHA256 of
// "<ts>:<body>" under the secret. Paddle sends several h1 values while it
// rotates a secret; any one matching any accepted secret is enough. The body
// must be the bytes as received: a body decoded and encoded again does not
// verify.
func (w *Webhook) Verify(header string, body []byte) error {
	if len(w.secrets) == 0 || header == "" {
		return ErrSignature
	}
	var ts string
	var sigs [][]byte
	for _, part := range strings.Split(header, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "ts":
			ts = v
		case "h1":
			if b, err := hex.DecodeString(v); err == nil {
				sigs = append(sigs, b)
			}
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return ErrSignature
	}

	matched := false
	for _, secret := range w.secrets {
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(ts))
		mac.Write([]byte(":"))
		mac.Write(body)
		want := mac.Sum(nil)
		for _, s := range sigs {
			if hmac.Equal(s, want) {
				matched = true
			}
		}
	}
	if !matched {
		return ErrSignature
	}

	// The age is checked after the signature, so a forged header learns
	// nothing from the error about the clock.
	age := w.now().Sub(time.Unix(sec, 0))
	if age > w.tolerance || age < -w.tolerance {
		return ErrSignatureExpired
	}
	return nil
}

// Parse verifies a delivery and decodes it.
func (w *Webhook) Parse(header string, body []byte) (*Event, error) {
	if err := w.Verify(header, body); err != nil {
		return nil, err
	}
	var e Event
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, fmt.Errorf("paddle: decode event: %w", err)
	}
	if e.ID == "" || e.Type == "" {
		return nil, errors.New("paddle: decode event: no event_id or event_type")
	}
	return &e, nil
}

// ParseRequest reads, verifies and decodes a delivery.
//
// Answer 2xx quickly once the event is stored: Paddle waits five seconds
// and then treats the delivery as failed and sends it again.
func (w *Webhook) ParseRequest(r *http.Request) (*Event, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, w.maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("paddle: read webhook: %w", err)
	}
	if int64(len(body)) > w.maxBody {
		return nil, fmt.Errorf("paddle: webhook body larger than %d bytes", w.maxBody)
	}
	return w.Parse(r.Header.Get("Paddle-Signature"), body)
}

// Sign returns a Paddle-Signature header for the body at the given time. It
// exists for tests of the code that receives webhooks.
func Sign(secret string, body []byte, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + ":"))
	mac.Write(body)
	return "ts=" + ts + ";h1=" + hex.EncodeToString(mac.Sum(nil))
}
