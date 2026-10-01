package paddle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var ctx = context.Background()

// recorded is one request the test server saw.
type recorded struct {
	Method, Path, Query string
	Header              http.Header
	Body                map[string]any
}

// server answers each request with the next reply and records what it got.
type server struct {
	t       *testing.T
	srv     *httptest.Server
	replies []reply
	got     []recorded
	n       atomic.Int64
}

type reply struct {
	status int
	body   string
	header map[string]string
}

func newServer(t *testing.T, replies ...reply) (*server, *Client) {
	t.Helper()
	s := &server{t: t, replies: replies}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &rec.Body); err != nil {
				t.Errorf("request body is not JSON: %s", raw)
			}
		}
		s.got = append(s.got, rec)
		i := int(s.n.Add(1)) - 1
		if i >= len(s.replies) {
			t.Errorf("unexpected request %d: %s %s", i+1, r.Method, r.URL)
			w.WriteHeader(500)
			return
		}
		rp := s.replies[i]
		for k, v := range rp.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rp.status)
		_, _ = io.WriteString(w, rp.body)
	}))
	t.Cleanup(s.srv.Close)
	c := New("pdl_live_apikey_test", WithBaseURL(s.srv.URL))
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return s, c
}

func ok(data string) reply {
	return reply{status: 200, body: `{"data":` + data + `,"meta":{"request_id":"r"}}`}
}

func TestNewPicksHostFromKey(t *testing.T) {
	if !New("pdl_sdbx_apikey_x").Sandbox() {
		t.Fatal("a sandbox key must talk to the sandbox")
	}
	if New("pdl_live_apikey_x").Sandbox() {
		t.Fatal("a live key must talk to production")
	}
	if New("pdl_live_apikey_x", WithSandbox(true)).Sandbox() != true {
		t.Fatal("WithSandbox(true) must override the key")
	}
	if New("pdl_sdbx_apikey_x", WithSandbox(false)).Sandbox() {
		t.Fatal("WithSandbox(false) must override the key")
	}
}

func TestCreateTransactionNonCatalog(t *testing.T) {
	s, c := newServer(t, reply{status: 201, body: `{"data":{"id":"txn_01","status":"ready",
		"custom_data":{"purchase":"p_9"},"checkout":{"url":"https://pay.example.com/?_ptxn=txn_01"},
		"details":{"totals":{"subtotal":"1000","tax":"200","grand_total":"1200","currency_code":"USD"}},
		"created_at":"2030-01-02T03:04:05.123456Z","updated_at":"2030-01-02T03:04:05Z"}}`})

	tx, err := c.CreateTransaction(ctx, &CreateTransactionRequest{
		Items: []TransactionItemRequest{{Price: &NonCatalogPrice{
			Description: "1000 credits",
			ProductID:   "pro_01",
			UnitPrice:   Money{Amount: "1000", CurrencyCode: "USD"},
			TaxMode:     TaxModeAccountSetting,
		}}},
		CustomData: map[string]any{"purchase": "p_9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tx.ID != "txn_01" || tx.Checkout.URL == "" || tx.Details.Totals.GrandTotal != "1200" || tx.CustomData["purchase"] != "p_9" {
		t.Fatalf("decoded transaction: %+v", tx)
	}
	if tx.CreatedAt.Nanosecond() != 123456000 {
		t.Fatalf("timestamps keep their fraction, got %s", tx.CreatedAt)
	}

	r := s.got[0]
	if r.Method != "POST" || r.Path != "/transactions" {
		t.Fatalf("got %s %s", r.Method, r.Path)
	}
	if r.Header.Get("Authorization") != "Bearer pdl_live_apikey_test" || r.Header.Get("Paddle-Version") != "1" ||
		r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers: %v", r.Header)
	}
	item := r.Body["items"].([]any)[0].(map[string]any)
	if item["quantity"].(float64) != 1 {
		t.Fatalf("quantity defaults to 1, got %v", item["quantity"])
	}
	price := item["price"].(map[string]any)
	if price["product_id"] != "pro_01" || price["unit_price"].(map[string]any)["amount"] != "1000" {
		t.Fatalf("non-catalog price: %v", price)
	}
	if _, has := item["price_id"]; has {
		t.Fatal("an unset price_id must not be sent")
	}
	if _, has := r.Body["customer_id"]; has {
		t.Fatal("an unset customer_id must not be sent")
	}
}

func TestCreateTransactionValidates(t *testing.T) {
	_, c := newServer(t)
	bad := []*CreateTransactionRequest{
		nil,
		{},
		{Items: []TransactionItemRequest{{}}},
		{Items: []TransactionItemRequest{{PriceID: "pri_1", Price: &NonCatalogPrice{Description: "x", ProductID: "p"}}}},
		{Items: []TransactionItemRequest{{Price: &NonCatalogPrice{ProductID: "p"}}}},
		{Items: []TransactionItemRequest{{Price: &NonCatalogPrice{Description: "x"}}}},
	}
	for i, req := range bad {
		if _, err := c.CreateTransaction(ctx, req); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
}

func TestErrorsAreTyped(t *testing.T) {
	_, c := newServer(t, reply{status: 400, body: `{"error":{"type":"request_error","code":"invalid_field",
		"detail":"Invalid request.","documentation_url":"https://developer.paddle.com/errors/shared/invalid_field",
		"errors":[{"field":"items[0].price.description","message":"is required"}]},"meta":{"request_id":"req-1"}}`},
		reply{status: 404, body: `{"error":{"type":"request_error","code":"not_found","detail":"Entity not found"},"meta":{"request_id":"req-2"}}`})

	_, err := c.GetTransaction(ctx, "txn_1")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Code != "invalid_field" || ae.RequestID != "req-1" ||
		len(ae.Errors) != 1 || ae.Errors[0].Field != "items[0].price.description" {
		t.Fatalf("want a typed 400, got %#v", err)
	}
	if !strings.Contains(err.Error(), "items[0].price.description: is required") {
		t.Fatalf("the message must name the field, got %q", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a 400 is not ErrNotFound")
	}
	_, err = c.GetSubscription(ctx, "sub_1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a 404 must match ErrNotFound, got %v", err)
	}
}

func TestRetries(t *testing.T) {
	t.Run("a GET is retried on 5xx", func(t *testing.T) {
		s, c := newServer(t, reply{status: 502, body: "bad gateway"}, reply{status: 503}, ok(`{"id":"sub_1","status":"active"}`))
		sub, err := c.GetSubscription(ctx, "sub_1")
		if err != nil || sub.Status != "active" || len(s.got) != 3 {
			t.Fatalf("want success on the third try, got %v after %d", err, len(s.got))
		}
	})
	t.Run("a POST is not retried on 5xx", func(t *testing.T) {
		s, c := newServer(t, reply{status: 500, body: `{"error":{"type":"api_error","code":"internal_error"}}`})
		_, err := c.RefundTransaction(ctx, "txn_1", "withdrawal")
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 500 || len(s.got) != 1 {
			t.Fatalf("a POST that may have run must not be repeated: %v after %d requests", err, len(s.got))
		}
	})
	t.Run("a POST is retried on 429 after Retry-After", func(t *testing.T) {
		s, c := newServer(t,
			reply{status: 429, body: `{"error":{"code":"too_many_requests"}}`, header: map[string]string{"Retry-After": "7"}},
			reply{status: 201, body: `{"data":{"id":"adj_1","status":"pending_approval"}}`})
		var waited []time.Duration
		c.sleep = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
		adj, err := c.RefundTransaction(ctx, "txn_1", "withdrawal")
		if err != nil || adj.ID != "adj_1" || len(s.got) != 2 {
			t.Fatalf("a refused request is safe to repeat: %v after %d", err, len(s.got))
		}
		if len(waited) != 1 || waited[0] != 7*time.Second {
			t.Fatalf("Retry-After must be honoured, waited %v", waited)
		}
	})
	t.Run("retries stop at the limit", func(t *testing.T) {
		s, c := newServer(t, reply{status: 503}, reply{status: 503}, reply{status: 503})
		if _, err := c.GetTransaction(ctx, "txn_1"); err == nil || len(s.got) != 3 {
			t.Fatalf("two retries means three requests, got %d (%v)", len(s.got), err)
		}
	})
	t.Run("a cancelled context stops the wait", func(t *testing.T) {
		_, c := newServer(t, reply{status: 503})
		cctx, cancel := context.WithCancel(ctx)
		c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return sleepCtx(ctx, d) }
		if _, err := c.GetTransaction(cctx, "txn_1"); !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	})
}

func TestSubscriptionCalls(t *testing.T) {
	sub := `{"id":"sub_1","status":"active","billing_cycle":{"interval":"year","frequency":1},
		"current_billing_period":{"starts_at":"2030-01-01T00:00:00Z","ends_at":"2031-01-01T00:00:00Z"},
		"scheduled_change":{"action":"cancel","effective_at":"2031-01-01T00:00:00Z","resume_at":null},
		"items":[{"status":"active","quantity":1,"price":{"id":"pri_year"}}],
		"management_urls":{"update_payment_method":"https://u","cancel":"https://c"},
		"next_billed_at":null,"created_at":"2030-01-01T00:00:00Z","updated_at":"2030-01-01T00:00:00Z"}`
	s, c := newServer(t, ok(sub), ok(sub), ok(sub), ok(sub), ok(sub))

	got, err := c.CancelSubscription(ctx, "sub_1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ScheduledChange == nil || got.ScheduledChange.Action != "cancel" || got.CurrentBillingPeriod.EndsAt.Year() != 2031 ||
		got.Items[0].Price.ID != "pri_year" || got.NextBilledAt != nil || got.ManagementURLs.Cancel != "https://c" {
		t.Fatalf("decoded subscription: %+v", got)
	}
	if s.got[0].Path != "/subscriptions/sub_1/cancel" || s.got[0].Body["effective_from"] != "next_billing_period" {
		t.Fatalf("cancel defaults to the end of the period: %+v", s.got[0])
	}

	if _, err := c.RemoveScheduledChange(ctx, "sub_1"); err != nil {
		t.Fatal(err)
	}
	r := s.got[1]
	if v, has := r.Body["scheduled_change"]; r.Method != "PATCH" || !has || v != nil {
		t.Fatalf("removing a scheduled change sends an explicit null: %s %+v", r.Method, r.Body)
	}

	resume := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.PauseSubscription(ctx, "sub_1", &PauseRequest{EffectiveFrom: EffectiveImmediately, ResumeAt: &resume}); err != nil {
		t.Fatal(err)
	}
	if b := s.got[2].Body; b["effective_from"] != "immediately" || b["resume_at"] != "2030-06-01T00:00:00Z" {
		t.Fatalf("pause body: %+v", b)
	}
	if _, err := c.ResumeSubscription(ctx, "sub_1", nil); err != nil {
		t.Fatal(err)
	}
	if b := s.got[3].Body; s.got[3].Path != "/subscriptions/sub_1/resume" || b["effective_from"] != "immediately" {
		t.Fatalf("resume defaults to immediately: %+v", b)
	}
	if _, err := c.GetSubscription(ctx, "sub_1"); err != nil || s.got[4].Method != "GET" {
		t.Fatal(err)
	}

	for _, id := range []string{"", "sub_1/../txn", "a?b"} {
		if _, err := c.GetSubscription(ctx, id); !errors.Is(err, ErrInvalid) {
			t.Errorf("id %q must be refused before it reaches the URL, got %v", id, err)
		}
	}
}

func TestRefund(t *testing.T) {
	s, c := newServer(t, reply{status: 201, body: `{"data":{"id":"adj_1","action":"refund","type":"full",
		"transaction_id":"txn_1","status":"pending_approval","totals":{"total":"1200","currency_code":"USD"},
		"created_at":"2030-01-01T00:00:00Z"}}`})
	adj, err := c.RefundTransaction(ctx, "txn_1", "Withdrawal within 14 days")
	if err != nil || adj.Status != AdjustmentPendingApproval || adj.Totals.Total != "1200" {
		t.Fatalf("got %+v, %v", adj, err)
	}
	b := s.got[0].Body
	if b["action"] != "refund" || b["type"] != "full" || b["transaction_id"] != "txn_1" || b["reason"] == "" {
		t.Fatalf("refund body: %+v", b)
	}
	if _, has := b["items"]; has {
		t.Fatal("a full refund sends no items")
	}
	if _, err := c.CreateAdjustment(ctx, &CreateAdjustmentRequest{Action: "refund", TransactionID: "txn_1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an adjustment needs a reason, got %v", err)
	}
}

func TestEnsureCustomer(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		s, c := newServer(t, reply{status: 201, body: `{"data":{"id":"ctm_1","email":"a@example.com"}}`})
		cu, err := c.EnsureCustomer(ctx, "a@example.com", "Ann")
		if err != nil || cu.ID != "ctm_1" || s.got[0].Body["name"] != "Ann" {
			t.Fatalf("got %+v, %v", cu, err)
		}
	})
	t.Run("existing", func(t *testing.T) {
		s, c := newServer(t,
			reply{status: 409, body: `{"error":{"type":"request_error","code":"customer_already_exists",
				"detail":"customer email conflicts with customer of id ctm_7"}}`},
			ok(`[{"id":"ctm_7","email":"a+x@example.com"}]`))
		cu, err := c.EnsureCustomer(ctx, "a+x@example.com", "")
		if err != nil || cu.ID != "ctm_7" {
			t.Fatalf("got %+v, %v", cu, err)
		}
		if s.got[1].Path != "/customers" || s.got[1].Query != "email=a%2Bx%40example.com&status=active%2Carchived" {
			t.Fatalf("the email must be escaped, and archived customers looked up too; got %q", s.got[1].Query)
		}
		if _, has := s.got[0].Body["name"]; has {
			t.Fatal("an empty name must not be sent")
		}
	})
	t.Run("not found", func(t *testing.T) {
		_, c := newServer(t, ok(`[]`))
		if _, err := c.CustomerByEmail(ctx, "b@example.com"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})
	t.Run("other error", func(t *testing.T) {
		_, c := newServer(t, reply{status: 400, body: `{"error":{"code":"invalid_field"}}`})
		var ae *APIError
		if _, err := c.EnsureCustomer(ctx, "bad", ""); !errors.As(err, &ae) || ae.Code != "invalid_field" {
			t.Fatalf("want the original error, got %v", err)
		}
	})
}

func TestPortalSession(t *testing.T) {
	s, c := newServer(t, reply{status: 201, body: `{"data":{"id":"cpls_1","customer_id":"ctm_1",
		"urls":{"general":{"overview":"https://portal/overview"},
		"subscriptions":[{"id":"sub_1","cancel_subscription":"https://portal/cancel","update_subscription_payment_method":"https://portal/pm"}]},
		"created_at":"2030-01-01T00:00:00Z"}}`})
	ps, err := c.CreatePortalSession(ctx, "ctm_1", "sub_1")
	if err != nil || ps.URLs.General.Overview != "https://portal/overview" || ps.URLs.Subscriptions[0].CancelSubscription != "https://portal/cancel" {
		t.Fatalf("got %+v, %v", ps, err)
	}
	if s.got[0].Path != "/customers/ctm_1/portal-sessions" || len(s.got[0].Body["subscription_ids"].([]any)) != 1 {
		t.Fatalf("request: %+v", s.got[0])
	}
}

func TestExtraHeaderAndVersion(t *testing.T) {
	s, c := newServer(t, ok(`{"id":"txn_1"}`))
	c2 := New("k", WithBaseURL(c.baseURL), WithVersion("2"), WithHeader("X-Trace", "t1"))
	if _, err := c2.GetTransaction(ctx, "txn_1"); err != nil {
		t.Fatal(err)
	}
	if h := s.got[0].Header; h.Get("Paddle-Version") != "2" || h.Get("X-Trace") != "t1" {
		t.Fatalf("headers: %v", h)
	}
}

func TestWithTimeoutLeavesCustomClientAlone(t *testing.T) {
	custom := &http.Client{Timeout: time.Minute}
	c := New("k", WithHTTPClient(custom), WithTimeout(time.Second))
	if c.http.Timeout != time.Minute || custom.Timeout != time.Minute || custom.CheckRedirect != nil {
		t.Fatal("WithTimeout must not change a client the caller owns, and New must not mutate it")
	}
	if New("k", WithTimeout(time.Second)).http.Timeout != time.Second {
		t.Fatal("WithTimeout must set the default client's timeout")
	}
}

func TestChangeSubscriptionItems(t *testing.T) {
	s, c := newServer(t, ok(`{"id":"sub_1","status":"active","items":[{"quantity":3,"price":{"id":"pri_year"}}]}`))
	sub, err := c.ChangeSubscriptionItems(ctx, "sub_1", []SubscriptionItemRequest{{PriceID: "pri_year", Quantity: 3}}, ProratedImmediately)
	if err != nil || sub.Items[0].Quantity != 3 {
		t.Fatalf("got %+v, %v", sub, err)
	}
	r := s.got[0]
	item := r.Body["items"].([]any)[0].(map[string]any)
	if r.Method != "PATCH" || r.Path != "/subscriptions/sub_1" || r.Body["proration_billing_mode"] != "prorated_immediately" ||
		item["price_id"] != "pri_year" || item["quantity"].(float64) != 3 {
		t.Fatalf("request: %s %s %+v", r.Method, r.Path, r.Body)
	}
	bad := map[string]error{}
	_, bad["no items"] = c.ChangeSubscriptionItems(ctx, "sub_1", nil, ProratedImmediately)
	_, bad["no mode"] = c.ChangeSubscriptionItems(ctx, "sub_1", []SubscriptionItemRequest{{PriceID: "p"}}, "")
	_, bad["no price"] = c.ChangeSubscriptionItems(ctx, "sub_1", []SubscriptionItemRequest{{Quantity: 2}}, DoNotBill)
	for name, err := range bad {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestCreateInvoice(t *testing.T) {
	s, c := newServer(t, reply{status: 201, body: `{"data":{"id":"txn_inv","status":"billed","collection_mode":"manual",
		"billing_details":{"enable_checkout":true,"payment_terms":{"interval":"day","frequency":30},"purchase_order_number":"PO-7"}}}`})
	tx, err := c.CreateTransaction(ctx, &CreateTransactionRequest{
		Items:          []TransactionItemRequest{{PriceID: "pri_team_year", Quantity: 10}},
		CustomerID:     "ctm_1",
		AddressID:      "add_1",
		BusinessID:     "biz_1",
		CollectionMode: "manual",
		BillingDetails: &BillingDetails{EnableCheckout: true, PaymentTerms: Duration{Interval: "day", Frequency: 30}, PurchaseOrderNumber: "PO-7"},
	})
	if err != nil || tx.BillingDetails == nil || tx.BillingDetails.PaymentTerms.Frequency != 30 {
		t.Fatalf("got %+v, %v", tx, err)
	}
	bd := s.got[0].Body["billing_details"].(map[string]any)
	if s.got[0].Body["collection_mode"] != "manual" || bd["purchase_order_number"] != "PO-7" || bd["enable_checkout"] != true {
		t.Fatalf("body: %+v", s.got[0].Body)
	}
	if _, err := c.CreateTransaction(ctx, &CreateTransactionRequest{
		Items: []TransactionItemRequest{{PriceID: "p"}}, CollectionMode: "manual",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a manual transaction without billing details is refused, got %v", err)
	}
}
