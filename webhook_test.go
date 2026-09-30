package paddle

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const secret = "pdl_ntfset_01_secret"

var sent = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

const completed = `{"event_id":"evt_01","event_type":"transaction.completed","occurred_at":"2030-01-02T03:04:05.000000Z",
"notification_id":"ntf_01","data":{"id":"txn_01","status":"completed","customer_id":"ctm_01",
"subscription_id":"sub_01","custom_data":{"purchase":"p_9"},"origin":"web","currency_code":"EUR",
"items":[{"quantity":1,"price":{"id":"pri_01","unit_price":{"amount":"1000","currency_code":"EUR"}}}],
"details":{"totals":{"subtotal":"1000","tax":"200","total":"1200","grand_total":"1200","currency_code":"EUR"}},
"created_at":"2030-01-02T03:00:00Z","updated_at":"2030-01-02T03:04:05Z"}}`

func webhook(opts ...WebhookOption) *Webhook {
	return NewWebhook(secret, append([]WebhookOption{WithWebhookClock(func() time.Time { return sent.Add(2 * time.Second) })}, opts...)...)
}

func TestWebhookParsesSignedEvent(t *testing.T) {
	ev, err := webhook().Parse(Sign(secret, []byte(completed), sent), []byte(completed))
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != "evt_01" || ev.Type != EventTransactionCompleted || ev.NotificationID != "ntf_01" || !ev.OccurredAt.Equal(sent) {
		t.Fatalf("envelope: %+v", ev)
	}
	tx, err := ev.Transaction()
	if err != nil {
		t.Fatal(err)
	}
	if tx.ID != "txn_01" || tx.SubscriptionID != "sub_01" || tx.CustomData["purchase"] != "p_9" ||
		tx.Details.Totals.Subtotal != "1000" || tx.Items[0].Price.UnitPrice.Amount != "1000" {
		t.Fatalf("transaction: %+v", tx)
	}
}

func TestWebhookRejects(t *testing.T) {
	body := []byte(completed)
	good := Sign(secret, body, sent)
	cases := map[string]struct {
		header string
		body   []byte
		wh     *Webhook
		want   error
	}{
		"empty header":                 {"", body, webhook(), ErrSignature},
		"no timestamp":                 {"h1=" + strings.Split(good, "h1=")[1], body, webhook(), ErrSignature},
		"no signature":                 {"ts=1", body, webhook(), ErrSignature},
		"wrong secret":                 {Sign("other", body, sent), body, webhook(), ErrSignature},
		"tampered body":                {good, bytes.Replace(body, []byte("1000"), []byte("9000"), 1), webhook(), ErrSignature},
		"re-encoded body":              {good, bytes.ReplaceAll(body, []byte("\n"), nil), webhook(), ErrSignature},
		"old delivery":                 {Sign(secret, body, sent.Add(-time.Minute)), body, webhook(), ErrSignatureExpired},
		"future delivery":              {Sign(secret, body, sent.Add(time.Minute)), body, webhook(), ErrSignatureExpired},
		"no secret":                    {good, body, NewWebhook(""), ErrSignature},
		"garbage h1":                   {"ts=" + strings.TrimPrefix(strings.Split(good, ";")[0], "ts=") + ";h1=zz", body, webhook(), ErrSignature},
		"garbage ts":                   {"ts=abc;h1=00", body, webhook(), ErrSignature},
		"expired matches ErrSignature": {Sign(secret, body, sent.Add(-time.Hour)), body, webhook(), ErrSignature},
	}
	for name, c := range cases {
		_, err := c.wh.Parse(c.header, c.body)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", name, c.want, err)
		}
	}
}

func TestWebhookRotation(t *testing.T) {
	body := []byte(completed)
	// During a rotation Paddle signs with both secrets.
	both := Sign("new-secret", body, sent) + ";h1=" + strings.Split(Sign(secret, body, sent), "h1=")[1]
	if _, err := webhook().Parse(both, body); err != nil {
		t.Fatalf("any one matching h1 is enough: %v", err)
	}
	wh := NewWebhook("new-secret", WithPreviousSecret(secret), WithWebhookClock(func() time.Time { return sent }))
	if _, err := wh.Parse(Sign(secret, body, sent), body); err != nil {
		t.Fatalf("the previous secret must still verify: %v", err)
	}
}

func TestWebhookTolerance(t *testing.T) {
	body := []byte(completed)
	h := Sign(secret, body, sent.Add(-30*time.Second))
	if _, err := webhook().Parse(h, body); !errors.Is(err, ErrSignatureExpired) {
		t.Fatalf("30s is outside the default tolerance, got %v", err)
	}
	if _, err := webhook(WithTolerance(time.Minute)).Parse(h, body); err != nil {
		t.Fatalf("30s is inside a minute: %v", err)
	}
}

func TestWebhookRequest(t *testing.T) {
	body := []byte(completed)
	r := httptest.NewRequest("POST", "/webhooks/paddle", bytes.NewReader(body))
	r.Header.Set("Paddle-Signature", Sign(secret, body, sent))
	if ev, err := webhook().ParseRequest(r); err != nil || ev.ID != "evt_01" {
		t.Fatalf("got %+v, %v", ev, err)
	}

	r = httptest.NewRequest("POST", "/webhooks/paddle", bytes.NewReader(body))
	r.Header.Set("Paddle-Signature", Sign(secret, body, sent))
	if _, err := webhook(WithMaxBody(10)).ParseRequest(r); err == nil || errors.Is(err, ErrSignature) {
		t.Fatalf("an oversized body is refused before verification, got %v", err)
	}
}

func TestWebhookEventShapes(t *testing.T) {
	wh := webhook()
	parse := func(body string) *Event {
		t.Helper()
		ev, err := wh.Parse(Sign(secret, []byte(body), sent), []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	adj, err := parse(`{"event_id":"evt_2","event_type":"adjustment.updated","occurred_at":"2030-01-02T03:04:05Z",
		"notification_id":"ntf_2","data":{"id":"adj_1","action":"chargeback","transaction_id":"txn_01","status":"approved",
		"reason":"fraud","created_at":"2030-01-02T03:00:00Z"}}`).Adjustment()
	if err != nil || adj.Action != ActionChargeback || adj.Status != AdjustmentApproved || adj.TransactionID != "txn_01" {
		t.Fatalf("adjustment: %+v, %v", adj, err)
	}

	sub, err := parse(`{"event_id":"evt_3","event_type":"subscription.canceled","occurred_at":"2030-01-02T03:04:05Z",
		"notification_id":"ntf_3","data":{"id":"sub_1","status":"canceled","customer_id":"ctm_1",
		"billing_cycle":{"interval":"year","frequency":2},"canceled_at":"2030-01-02T03:04:05Z",
		"created_at":"2029-01-01T00:00:00Z","updated_at":"2030-01-02T03:04:05Z"}}`).Subscription()
	if err != nil || sub.Status != SubscriptionCanceled || sub.BillingCycle.Frequency != 2 || sub.CanceledAt == nil {
		t.Fatalf("subscription: %+v, %v", sub, err)
	}

	cu, err := parse(`{"event_id":"evt_4","event_type":"customer.updated","occurred_at":"2030-01-02T03:04:05Z",
		"notification_id":"ntf_4","data":{"id":"ctm_1","email":"a@example.com","created_at":"2029-01-01T00:00:00Z",
		"updated_at":"2030-01-02T03:04:05Z"}}`).Customer()
	if err != nil || cu.Email != "a@example.com" {
		t.Fatalf("customer: %+v, %v", cu, err)
	}

	ev := parse(`{"event_id":"evt_5","event_type":"payout.paid","occurred_at":"2030-01-02T03:04:05Z","notification_id":"ntf_5","data":{"id":"x"}}`)
	var generic map[string]any
	if err := ev.Decode(&generic); err != nil || generic["id"] != "x" {
		t.Fatalf("an unnamed event type still decodes: %v", err)
	}

	body := `{"event_type":"transaction.paid","data":{}}`
	if _, err := wh.Parse(Sign(secret, []byte(body), sent), []byte(body)); err == nil {
		t.Fatal("an event without an id cannot be deduplicated and must be refused")
	}
	body = `not json`
	if _, err := wh.Parse(Sign(secret, []byte(body), sent), []byte(body)); err == nil || errors.Is(err, ErrSignature) {
		t.Fatalf("a signed body that is not JSON is a decode error, got %v", err)
	}
}
