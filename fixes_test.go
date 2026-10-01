package paddle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRedirectIsNotFollowed(t *testing.T) {
	var hit atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		fmt.Fprint(w, `{"data":{"id":"txn_evil"}}`)
	}))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
	}))
	defer api.Close()

	for name, c := range map[string]*Client{
		"default client": New("pdl_live_k", WithBaseURL(api.URL)),
		"custom client":  New("pdl_live_k", WithBaseURL(api.URL), WithHTTPClient(&http.Client{})),
	} {
		_, err := c.CreateTransaction(ctx, &CreateTransactionRequest{Items: []TransactionItemRequest{{PriceID: "pri_1"}}})
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusTemporaryRedirect {
			t.Errorf("%s: a redirect must come back as an error, got %v", name, err)
		}
	}
	if hit.Load() != 0 {
		t.Fatal("the request must never reach the host a redirect names")
	}
}

func TestIDsAreAllowlisted(t *testing.T) {
	c := New("pdl_live_k", WithBaseURL("http://127.0.0.1:1"))
	for _, id := range []string{"..", "%2e%2e", "sub_1%2F..", "a b", "a\nb", "sub_1;x", `a\b`, "é", strings.Repeat("a", maxIDLength+1)} {
		if _, err := c.GetSubscription(ctx, id); !errors.Is(err, ErrInvalid) {
			t.Errorf("id %q must be refused before it reaches the path, got %v", id, err)
		}
	}
	if err := checkID("subscription", "sub_01h8-ABC_z9"); err != nil {
		t.Fatalf("a real id must pass, got %v", err)
	}
}

func TestBadRequestIsNotRetried(t *testing.T) {
	var sleeps atomic.Int64
	c := New("pdl_live_k", WithBaseURL("http://127.0.0.1:1"), WithMaxRetries(5))
	c.sleep = func(context.Context, time.Duration) error { sleeps.Add(1); return nil }
	c.baseURL = "http://127.0.0.1:1/\x7f"
	err := c.do(ctx, http.MethodGet, "/transactions/txn_1", nil, nil)
	if !errors.Is(err, ErrInvalid) || sleeps.Load() != 0 {
		t.Fatalf("a request that cannot be built is returned at once, got %v after %d sleeps", err, sleeps.Load())
	}
}

func TestBackoffNeverPanics(t *testing.T) {
	for attempt := -1; attempt <= 100; attempt++ {
		if d := backoff(attempt); d <= 0 || d > 8*time.Second {
			t.Fatalf("backoff(%d) = %v, want (0, 8s]", attempt, d)
		}
	}
	if c := New("k", WithMaxRetries(1000)); c.maxRetries != MaxRetries {
		t.Fatalf("WithMaxRetries must be capped at %d, got %d", MaxRetries, c.maxRetries)
	}
	if c := New("k", WithMaxRetries(-3)); c.maxRetries != 0 {
		t.Fatalf("a negative retry count is 0, got %d", c.maxRetries)
	}
}

func TestSecretsAreNotPrinted(t *testing.T) {
	c := New("pdl_live_secret_key")
	wh := NewWebhook("pdl_ntfset_secret", WithPreviousSecret("old_secret"))
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "client", c, "webhook", wh)
	out := fmt.Sprintf("%v %+v %#v %s %v %+v %#v %s", c, c, c, c, wh, wh, wh, wh) + buf.String()
	for _, secret := range []string{"pdl_live_secret_key", "pdl_ntfset_secret", "old_secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%q leaked into %s", secret, out)
		}
	}
	if !strings.Contains(out, ProductionURL) {
		t.Fatalf("the client should still say where it talks, got %s", out)
	}
}

func TestUnsafeConfigIsRefused(t *testing.T) {
	if _, err := New("", WithBaseURL("https://api.example.com")).GetTransaction(ctx, "txn_1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty key must be refused, got %v", err)
	}
	if _, err := New("k", WithBaseURL("http://api.example.com")).GetTransaction(ctx, "txn_1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plain HTTP to a remote host must be refused, got %v", err)
	}
	if _, err := New("k", WithBaseURL("::bad")).GetTransaction(ctx, "txn_1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a malformed base URL must be refused, got %v", err)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		if err := New("k", WithBaseURL("http://"+host+":8080")).check(); err != nil {
			t.Fatalf("plain HTTP to %s is a test server and is allowed, got %v", host, err)
		}
	}
	c := New("k", WithHTTPClient(nil))
	if c.http == nil || c.http.Timeout != DefaultTimeout {
		t.Fatal("WithHTTPClient(nil) must keep the default client")
	}
}

func TestOversizedResponse(t *testing.T) {
	big := `{"data":{"id":"txn_1","custom_data":{"x":"` + strings.Repeat("a", maxResponse) + `"}}}`
	_, c := newServer(t, ok(`{}`))
	c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: ioNop(big), Header: http.Header{}}, nil
	})
	if _, err := c.CreateTransaction(ctx, &CreateTransactionRequest{Items: []TransactionItemRequest{{PriceID: "pri_1"}}}); err == nil ||
		!strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a response beyond the limit must say so, got %v", err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type nopBody struct{ *strings.Reader }

func (nopBody) Close() error { return nil }

func ioNop(s string) nopBody { return nopBody{strings.NewReader(s)} }

func TestNetworkErrorRetriedOnGetOnly(t *testing.T) {
	var calls atomic.Int64
	fail := roundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	})
	c := New("k", WithBaseURL("https://api.example.com"), WithHTTPClient(&http.Client{Transport: fail}))
	c.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := c.GetTransaction(ctx, "txn_1"); err == nil {
		t.Fatal("want an error")
	}
	if calls.Load() != 1+DefaultMaxRetries {
		t.Fatalf("a GET is retried after a network error: want %d calls, got %d", 1+DefaultMaxRetries, calls.Load())
	}
	calls.Store(0)
	if _, err := c.CancelSubscription(ctx, "sub_1", ""); err == nil || calls.Load() != 1 {
		t.Fatalf("a POST is never retried after a network error, got %d calls, %v", calls.Load(), err)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.GetTransaction(cctx, "txn_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context is returned as it is, got %v", err)
	}
}

func TestSleepStopsOnCancel(t *testing.T) {
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := sleepCtx(cctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if err := sleepCtx(ctx, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if retryAfter("abc") != 0 || retryAfter("-1") != 0 || retryAfter("3") != 3*time.Second || retryAfter("3600") != maxRetryAfter {
		t.Fatal("retryAfter")
	}
}

func TestEnsureCustomerReactivatesArchived(t *testing.T) {
	s, c := newServer(t,
		reply{status: 409, body: `{"error":{"code":"customer_already_exists"}}`},
		ok(`[{"id":"ctm_7","email":"ann@example.com","status":"archived"}]`),
		ok(`{"id":"ctm_7","email":"ann@example.com","status":"active"}`))
	cu, err := c.EnsureCustomer(ctx, "ann@example.com", "Ann")
	if err != nil || cu.Status != CustomerActive {
		t.Fatalf("an archived customer must be brought back, got %+v, %v", cu, err)
	}
	if s.got[2].Method != http.MethodPatch || s.got[2].Path != "/customers/ctm_7" || s.got[2].Body["status"] != "active" {
		t.Fatalf("got %+v", s.got[2])
	}
}

func TestCustomerByEmailComparesTheAnswer(t *testing.T) {
	_, c := newServer(t, ok(`[{"id":"ctm_other","email":"someone-else@example.com"}]`))
	if _, err := c.CustomerByEmail(ctx, "ann@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a customer with another address must never be returned, got %v", err)
	}
	_, c = newServer(t, ok(`[{"id":"ctm_1","email":"Ann@Example.com"}]`))
	if cu, err := c.CustomerByEmail(ctx, "ann@example.com"); err != nil || cu.ID != "ctm_1" {
		t.Fatalf("the address is compared without case, got %+v, %v", cu, err)
	}
	if _, err := c.CustomerByEmail(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestGetCustomer(t *testing.T) {
	s, c := newServer(t, ok(`{"id":"ctm_1","email":"a@example.com"}`))
	cu, err := c.GetCustomer(ctx, "ctm_1")
	if err != nil || cu.Email != "a@example.com" || s.got[0].Path != "/customers/ctm_1" {
		t.Fatalf("got %+v, %v", cu, err)
	}
	if _, err := c.GetCustomer(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestParseAmount(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "1000": 1000, "-250": -250, "9223372036854775807": 1<<63 - 1} {
		if got, err := ParseAmount(in); err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "-", "10.5", "1e3", " 10", "+5", "9223372036854775808", "0x10"} {
		if _, err := ParseAmount(in); err == nil {
			t.Errorf("ParseAmount(%q) must fail", in)
		}
	}
	if n, err := (Money{Amount: "1500", CurrencyCode: "EUR"}).Int64(); err != nil || n != 1500 {
		t.Fatal(n, err)
	}
}

func TestLineItems(t *testing.T) {
	_, c := newServer(t, ok(`{"id":"txn_1","details":{"totals":{"total":"1200"},"line_items":[
		{"id":"txnitm_1","price_id":"pri_1","quantity":2,"totals":{"subtotal":"1000","tax":"200","total":"1200"}}]}}`))
	tx, err := c.GetTransaction(ctx, "txn_1")
	if err != nil || len(tx.Details.LineItems) != 1 {
		t.Fatalf("got %+v, %v", tx, err)
	}
	li := tx.Details.LineItems[0]
	if li.ID != "txnitm_1" || li.PriceID != "pri_1" || li.Quantity != 2 || li.Totals.Total != "1200" {
		t.Fatalf("a line must carry the id a partial refund names, got %+v", li)
	}
}

func TestListTransactions(t *testing.T) {
	next := `"meta":{"pagination":{"per_page":2,"next":"https://api.paddle.com/transactions?after=txn_2&per_page=2","has_more":true}}`
	last := `"meta":{"pagination":{"per_page":2,"next":"https://api.paddle.com/transactions?after=txn_3&per_page=2","has_more":false}}`
	s, c := newServer(t,
		reply{status: 200, body: `{"data":[{"id":"txn_1"},{"id":"txn_2"}],` + next + `}`},
		reply{status: 200, body: `{"data":[{"id":"txn_3"}],` + last + `}`})
	from := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	req := &ListTransactionsRequest{Status: []string{TransactionCompleted, TransactionPaid}, UpdatedFrom: from, OrderBy: "updated_at[ASC]", PerPage: 2}
	p, err := c.ListTransactions(ctx, req)
	if err != nil || len(p.Items) != 2 || !p.HasMore || p.After != "txn_2" {
		t.Fatalf("got %+v, %v", p, err)
	}
	q := s.got[0].Query
	for _, part := range []string{"status=completed%2Cpaid", "updated_at%5BGTE%5D=2030-01-02T03%3A04%3A05Z", "order_by=updated_at%5BASC%5D", "per_page=2"} {
		if !strings.Contains(q, part) {
			t.Errorf("query %q lacks %q", q, part)
		}
	}
	req.After = p.After
	p, err = c.ListTransactions(ctx, req)
	if err != nil || len(p.Items) != 1 || p.HasMore || p.After != "" {
		t.Fatalf("the last page names no next one, got %+v, %v", p, err)
	}
	if !strings.Contains(s.got[1].Query, "after=txn_2") {
		t.Fatalf("after must be sent, got %q", s.got[1].Query)
	}

	if _, err := c.ListTransactions(ctx, &ListTransactionsRequest{PerPage: 31}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := c.ListTransactions(ctx, &ListTransactionsRequest{CustomerID: "../x"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestListWithoutHasMore(t *testing.T) {
	_, c := newServer(t,
		reply{status: 200, body: `{"data":[{"id":"txn_1"},{"id":"txn_2"}],"meta":{"pagination":{"per_page":2,"next":"https://x/transactions?after=txn_2"}}}`},
		reply{status: 200, body: `{"data":[{"id":"txn_3"}],"meta":{"pagination":{"per_page":2,"next":"https://x/transactions?after=txn_3"}}}`},
		ok(`[]`))
	p, err := c.ListTransactions(ctx, nil)
	if err != nil || !p.HasMore || p.After != "txn_2" {
		t.Fatalf("a full page that names a next one may have more, got %+v, %v", p, err)
	}
	p, err = c.ListTransactions(ctx, &ListTransactionsRequest{After: p.After})
	if err != nil || p.HasMore {
		t.Fatalf("a short page is the last, got %+v, %v", p, err)
	}
	p, err = c.ListTransactions(ctx, nil)
	if err != nil || p.HasMore || len(p.Items) != 0 {
		t.Fatalf("no meta is one page, got %+v, %v", p, err)
	}
}

func TestListAdjustments(t *testing.T) {
	s, c := newServer(t, reply{status: 200, body: `{"data":[{"id":"adj_1","action":"chargeback","status":"approved"}],
		"meta":{"pagination":{"per_page":50,"next":"https://x/adjustments?after=adj_1","has_more":false}}}`})
	p, err := c.ListAdjustments(ctx, &ListAdjustmentsRequest{
		Action: []string{ActionRefund, ActionChargeback}, Status: []string{AdjustmentApproved},
		TransactionID: "txn_1", PerPage: 50,
	})
	if err != nil || len(p.Items) != 1 || p.Items[0].Action != ActionChargeback || p.HasMore {
		t.Fatalf("got %+v, %v", p, err)
	}
	q := s.got[0].Query
	for _, part := range []string{"action=refund%2Cchargeback", "status=approved", "transaction_id=txn_1", "per_page=50"} {
		if !strings.Contains(q, part) {
			t.Errorf("query %q lacks %q", q, part)
		}
	}
	if _, err := c.ListAdjustments(ctx, &ListAdjustmentsRequest{PerPage: 51}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := c.ListAdjustments(ctx, &ListAdjustmentsRequest{After: "a b"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestPauseResumeDefaults(t *testing.T) {
	s, c := newServer(t, ok(`{"id":"sub_1","status":"paused"}`), ok(`{"id":"sub_1","status":"active"}`),
		ok(`{"id":"sub_1"}`))
	if _, err := c.PauseSubscription(ctx, "sub_1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResumeSubscription(ctx, "sub_1", &ResumeRequest{OnResume: "continue_existing_billing_period"}); err != nil {
		t.Fatal(err)
	}
	if s.got[1].Body["effective_from"] != EffectiveImmediately || s.got[1].Body["on_resume"] != "continue_existing_billing_period" {
		t.Fatalf("an empty EffectiveFrom resumes now, got %+v", s.got[1].Body)
	}
	if _, err := c.RemoveScheduledChange(ctx, "sub_1"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() error{
		func() error { _, err := c.PauseSubscription(ctx, "", nil); return err },
		func() error { _, err := c.ResumeSubscription(ctx, "", nil); return err },
		func() error { _, err := c.RemoveScheduledChange(ctx, ""); return err },
		func() error { _, err := c.CancelSubscription(ctx, "", ""); return err },
		func() error { _, err := c.GetTransaction(ctx, ""); return err },
		func() error { _, err := c.CreatePortalSession(ctx, ""); return err },
		func() error { _, err := c.ChangeSubscriptionItems(ctx, "", nil, ""); return err },
	} {
		if err := f(); !errors.Is(err, ErrInvalid) {
			t.Errorf("an empty id must be refused, got %v", err)
		}
	}
}

func TestDecodeError(t *testing.T) {
	ev := &Event{Type: EventTransactionCompleted, Data: []byte(`{"id":5}`)}
	if _, err := ev.Transaction(); err == nil || !strings.Contains(err.Error(), EventTransactionCompleted) {
		t.Fatalf("a decode error names the event type, got %v", err)
	}
}
