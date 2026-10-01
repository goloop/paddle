package paddle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NonCatalogPrice is a price created for one transaction and never added to
// the catalog. It is how an amount decided on the server - a credit pack, a
// custom order - is charged without a catalog entry for every amount.
type NonCatalogPrice struct {
	// Description is shown to the customer on the checkout and the invoice;
	// it is required. Name is optional.
	Description string `json:"description"`
	Name        string `json:"name,omitempty"`

	// ProductID is the catalog product the price belongs to; it is required.
	ProductID string `json:"product_id"`

	UnitPrice Money `json:"unit_price"`

	// TaxMode defaults to TaxModeAccountSetting.
	TaxMode string `json:"tax_mode,omitempty"`

	// BillingCycle makes the price recurring; nil is a one-time price.
	BillingCycle *Duration      `json:"billing_cycle,omitempty"`
	TrialPeriod  *Duration      `json:"trial_period,omitempty"`
	Quantity     *QuantityRange `json:"quantity,omitempty"`
	CustomData   map[string]any `json:"custom_data,omitempty"`
}

// TransactionItemRequest is one line of a new transaction: a catalog price by
// id, or a non-catalog price. Exactly one of the two is set.
type TransactionItemRequest struct {
	PriceID string           `json:"price_id,omitempty"`
	Price   *NonCatalogPrice `json:"price,omitempty"`

	// Quantity defaults to 1.
	Quantity int `json:"quantity"`
}

// CreateTransactionRequest creates a transaction for the customer to pay.
type CreateTransactionRequest struct {
	Items []TransactionItemRequest `json:"items"`

	// CustomerID attaches an existing customer; without it the checkout
	// asks for an email. The API takes no email here: find or create the
	// customer first (EnsureCustomer) when the address is known.
	CustomerID   string `json:"customer_id,omitempty"`
	AddressID    string `json:"address_id,omitempty"`
	BusinessID   string `json:"business_id,omitempty"`
	CurrencyCode string `json:"currency_code,omitempty"`
	DiscountID   string `json:"discount_id,omitempty"`

	// CollectionMode is "automatic" (the default, a checkout) or "manual"
	// (an invoice). A manual transaction needs BillingDetails, and its
	// customer needs a name.
	CollectionMode string          `json:"collection_mode,omitempty"`
	BillingDetails *BillingDetails `json:"billing_details,omitempty"`

	// CustomData is returned on every event about the transaction. Put the
	// id of your own purchase row here, so a webhook can be matched even
	// when the transaction id was never saved.
	CustomData map[string]any `json:"custom_data,omitempty"`

	// Checkout names the page the checkout opens on; it must be an
	// approved domain.
	Checkout *CheckoutLink `json:"checkout,omitempty"`
}

// BillingDetails are the terms of an invoice, for a transaction or a
// subscription with manual collection.
type BillingDetails struct {
	// EnableCheckout lets the customer pay the invoice by card through a
	// checkout link as well as by bank transfer.
	EnableCheckout bool `json:"enable_checkout"`

	// PaymentTerms is how long the customer has to pay: 30 days is
	// {Interval: "day", Frequency: 30}.
	PaymentTerms Duration `json:"payment_terms"`

	PurchaseOrderNumber   string `json:"purchase_order_number,omitempty"`
	AdditionalInformation string `json:"additional_information,omitempty"`
}

// CreateTransaction creates a transaction. Open the checkout with the
// returned id (Paddle.js) or its Checkout.URL.
//
// It is not retried on a 5xx: see the package documentation.
func (c *Client) CreateTransaction(ctx context.Context, req *CreateTransactionRequest) (*Transaction, error) {
	if req == nil || len(req.Items) == 0 {
		return nil, fmt.Errorf("%w: a transaction needs at least one item", ErrInvalid)
	}
	if req.CollectionMode == "manual" && req.BillingDetails == nil {
		return nil, fmt.Errorf("%w: a manual transaction needs billing details", ErrInvalid)
	}
	body := *req
	body.Items = make([]TransactionItemRequest, len(req.Items))
	for i, it := range req.Items {
		switch {
		case (it.PriceID == "") == (it.Price == nil):
			return nil, fmt.Errorf("%w: item %d needs exactly one of PriceID and Price", ErrInvalid, i)
		case it.Price != nil && it.Price.Description == "":
			return nil, fmt.Errorf("%w: item %d: a non-catalog price needs a description", ErrInvalid, i)
		case it.Price != nil && it.Price.ProductID == "":
			return nil, fmt.Errorf("%w: item %d: a non-catalog price needs a product id", ErrInvalid, i)
		}
		if it.Quantity < 0 {
			return nil, fmt.Errorf("%w: item %d has a negative quantity", ErrInvalid, i)
		}
		if it.Quantity == 0 {
			it.Quantity = 1
		}
		body.Items[i] = it
	}
	var out Transaction
	if err := c.do(ctx, http.MethodPost, "/transactions", &body, &out); err != nil {
		return nil, err
	}
	if err := expectID("transaction", out.ID, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTransaction returns a transaction.
func (c *Client) GetTransaction(ctx context.Context, id string) (*Transaction, error) {
	if err := checkID("transaction", id); err != nil {
		return nil, err
	}
	var out Transaction
	if err := c.do(ctx, http.MethodGet, "/transactions/"+id, nil, &out); err != nil {
		return nil, err
	}
	if err := expectID("transaction", out.ID, id); err != nil {
		return nil, err
	}
	return &out, nil
}

// When a subscription change takes effect.
const (
	EffectiveImmediately       = "immediately"
	EffectiveNextBillingPeriod = "next_billing_period"
)

// GetSubscription returns a subscription.
func (c *Client) GetSubscription(ctx context.Context, id string) (*Subscription, error) {
	if err := checkID("subscription", id); err != nil {
		return nil, err
	}
	var out Subscription
	if err := c.do(ctx, http.MethodGet, "/subscriptions/"+id, nil, &out); err != nil {
		return nil, err
	}
	if err := expectID("subscription", out.ID, id); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelSubscription cancels a subscription, at the end of the billing
// period (EffectiveNextBillingPeriod, the default when effectiveFrom is
// empty) or at once (EffectiveImmediately).
//
// A cancellation at the end of the period leaves the subscription active
// with a ScheduledChange, and RemoveScheduledChange takes it back. An
// immediate one is final: a canceled subscription cannot be reinstated.
func (c *Client) CancelSubscription(ctx context.Context, id, effectiveFrom string) (*Subscription, error) {
	if err := checkID("subscription", id); err != nil {
		return nil, err
	}
	if effectiveFrom == "" {
		effectiveFrom = EffectiveNextBillingPeriod
	}
	body := map[string]string{"effective_from": effectiveFrom}
	var out Subscription
	if err := c.do(ctx, http.MethodPost, "/subscriptions/"+id+"/cancel", body, &out); err != nil {
		return nil, err
	}
	if err := expectID("subscription", out.ID, id); err != nil {
		return nil, err
	}
	return &out, nil
}

// PauseRequest pauses a subscription.
type PauseRequest struct {
	// EffectiveFrom defaults to EffectiveNextBillingPeriod.
	EffectiveFrom string `json:"effective_from,omitempty"`

	// ResumeAt resumes the subscription on its own; nil pauses it until it
	// is resumed.
	ResumeAt *time.Time `json:"resume_at,omitempty"`

	// OnResume is "start_new_billing_period" (the default) or
	// "continue_existing_billing_period".
	OnResume string `json:"on_resume,omitempty"`
}

// PauseSubscription pauses a subscription. req may be nil.
func (c *Client) PauseSubscription(ctx context.Context, id string, req *PauseRequest) (*Subscription, error) {
	if err := checkID("subscription", id); err != nil {
		return nil, err
	}
	if req == nil {
		req = &PauseRequest{}
	}
	var out Subscription
	if err := c.do(ctx, http.MethodPost, "/subscriptions/"+id+"/pause", req, &out); err != nil {
		return nil, err
	}
	if err := expectID("subscription", out.ID, id); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResumeRequest resumes a paused subscription.
type ResumeRequest struct {
	// EffectiveFrom is EffectiveImmediately (the default) or an RFC 3339
	// time.
	EffectiveFrom string `json:"effective_from"`

	// OnResume is "start_new_billing_period" (the default) or
	// "continue_existing_billing_period".
	OnResume string `json:"on_resume,omitempty"`
}

// ResumeSubscription resumes a paused subscription. req may be nil.
func (c *Client) ResumeSubscription(ctx context.Context, id string, req *ResumeRequest) (*Subscription, error) {
	if err := checkID("subscription", id); err != nil {
		return nil, err
	}
	body := ResumeRequest{EffectiveFrom: EffectiveImmediately}
	if req != nil {
		body = *req
		if body.EffectiveFrom == "" {
			body.EffectiveFrom = EffectiveImmediately
		}
	}
	var out Subscription
	if err := c.do(ctx, http.MethodPost, "/subscriptions/"+id+"/resume", body, &out); err != nil {
		return nil, err
	}
	if err := expectID("subscription", out.ID, id); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveScheduledChange takes back a cancellation or a pause scheduled for
// the end of the period: the customer who pressed "cancel" and changed
// their mind before the period ended.
func (c *Client) RemoveScheduledChange(ctx context.Context, id string) (*Subscription, error) {
	if err := checkID("subscription", id); err != nil {
		return nil, err
	}
	body := map[string]any{"scheduled_change": nil}
	var out Subscription
	if err := c.do(ctx, http.MethodPatch, "/subscriptions/"+id, body, &out); err != nil {
		return nil, err
	}
	if err := expectID("subscription", out.ID, id); err != nil {
		return nil, err
	}
	return &out, nil
}

// Proration billing modes: how a change to a subscription's items is billed.
const (
	ProratedImmediately       = "prorated_immediately"
	ProratedNextBillingPeriod = "prorated_next_billing_period"
	FullImmediately           = "full_immediately"
	FullNextBillingPeriod     = "full_next_billing_period"
	DoNotBill                 = "do_not_bill"
)

// SubscriptionItemRequest is one price a subscription should bill after an
// update.
type SubscriptionItemRequest struct {
	PriceID string `json:"price_id"`

	// Quantity defaults to 1; it is the number of seats on a per-seat price.
	Quantity int `json:"quantity"`
}

// ChangeSubscriptionItems replaces the prices a subscription bills: a move
// from the monthly to the yearly plan, from Personal to Team, or a change in
// the number of seats. prorationMode says how the difference is billed and
// is required by the API; ProratedImmediately charges or credits the
// difference now.
//
// The items listed are the whole new set: an item left out is removed.
func (c *Client) ChangeSubscriptionItems(ctx context.Context, id string, items []SubscriptionItemRequest, prorationMode string) (*Subscription, error) {
	if err := checkID("subscription", id); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: a subscription needs at least one item", ErrInvalid)
	}
	if prorationMode == "" {
		return nil, fmt.Errorf("%w: a change of items needs a proration mode", ErrInvalid)
	}
	list := make([]SubscriptionItemRequest, len(items))
	for i, it := range items {
		if it.PriceID == "" {
			return nil, fmt.Errorf("%w: item %d needs a price id", ErrInvalid, i)
		}
		if it.Quantity < 0 {
			return nil, fmt.Errorf("%w: item %d has a negative quantity", ErrInvalid, i)
		}
		if it.Quantity == 0 {
			it.Quantity = 1
		}
		list[i] = it
	}
	body := map[string]any{"items": list, "proration_billing_mode": prorationMode}
	var out Subscription
	if err := c.do(ctx, http.MethodPatch, "/subscriptions/"+id, body, &out); err != nil {
		return nil, err
	}
	if err := expectID("subscription", out.ID, id); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateAdjustmentRequest creates an adjustment.
type CreateAdjustmentRequest struct {
	// Action is ActionRefund or ActionCredit; the chargeback actions are
	// Paddle's to make, not yours.
	Action        string `json:"action"`
	TransactionID string `json:"transaction_id"`
	Reason        string `json:"reason"`

	// Type is "full" or "partial" (the API's default). A partial
	// adjustment lists its Items.
	Type  string           `json:"type,omitempty"`
	Items []AdjustmentItem `json:"items,omitempty"`

	// TaxMode is "internal" (the default: amounts include tax) or
	// "external".
	TaxMode string `json:"tax_mode,omitempty"`
}

// CreateAdjustment creates an adjustment. Most refunds start as
// AdjustmentPendingApproval, and adjustment.updated reports the decision;
// some are approved at once and send only adjustment.created. A handler acts
// on both events when the status is AdjustmentApproved.
//
// It is not retried on a 5xx: see the package documentation.
func (c *Client) CreateAdjustment(ctx context.Context, req *CreateAdjustmentRequest) (*Adjustment, error) {
	if req == nil || req.Action == "" || req.Reason == "" {
		return nil, fmt.Errorf("%w: an adjustment needs an action and a reason", ErrInvalid)
	}
	if err := checkID("transaction", req.TransactionID); err != nil {
		return nil, err
	}
	var out Adjustment
	if err := c.do(ctx, http.MethodPost, "/adjustments", req, &out); err != nil {
		return nil, err
	}
	if err := expectID("adjustment", out.ID, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefundTransaction asks for a full refund of a completed transaction.
func (c *Client) RefundTransaction(ctx context.Context, transactionID, reason string) (*Adjustment, error) {
	return c.CreateAdjustment(ctx, &CreateAdjustmentRequest{
		Action:        ActionRefund,
		Type:          "full",
		TransactionID: transactionID,
		Reason:        reason,
	})
}

// CreateCustomerRequest creates a customer.
type CreateCustomerRequest struct {
	Email      string         `json:"email"`
	Name       string         `json:"name,omitempty"`
	Locale     string         `json:"locale,omitempty"`
	CustomData map[string]any `json:"custom_data,omitempty"`
}

// CreateCustomer creates a customer. An address that already belongs to a
// customer fails with an *APIError whose Code is "customer_already_exists";
// EnsureCustomer handles that case.
func (c *Client) CreateCustomer(ctx context.Context, req *CreateCustomerRequest) (*Customer, error) {
	if req == nil || req.Email == "" {
		return nil, fmt.Errorf("%w: a customer needs an email", ErrInvalid)
	}
	var out Customer
	if err := c.do(ctx, http.MethodPost, "/customers", req, &out); err != nil {
		return nil, err
	}
	if err := expectID("customer", out.ID, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCustomer returns a customer.
func (c *Client) GetCustomer(ctx context.Context, id string) (*Customer, error) {
	if err := checkID("customer", id); err != nil {
		return nil, err
	}
	var out Customer
	if err := c.do(ctx, http.MethodGet, "/customers/"+id, nil, &out); err != nil {
		return nil, err
	}
	if err := expectID("customer", out.ID, id); err != nil {
		return nil, err
	}
	return &out, nil
}

// CustomerByEmail returns the customer with the address, active or archived,
// or ErrNotFound. The address is compared in the answer as well as in the
// query: a customer with another address is never returned.
//
// Call it only with an address the user has proved they own. The customer it
// returns opens portal sessions, with the invoices, addresses and
// subscriptions of whoever registered the address at Paddle.
func (c *Client) CustomerByEmail(ctx context.Context, email string) (*Customer, error) {
	if email == "" {
		return nil, fmt.Errorf("%w: empty email", ErrInvalid)
	}
	q := url.Values{"email": {email}, "status": {CustomerActive + "," + CustomerArchived}}
	var out []Customer
	if err := c.do(ctx, http.MethodGet, "/customers?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	for i := range out {
		if strings.EqualFold(out[i].Email, email) {
			return &out[i], nil
		}
	}
	return nil, fmt.Errorf("%w: no customer with email %q", ErrNotFound, email)
}

// EnsureCustomer returns the active customer with the address, creating it
// when there is none and bringing it back when it was archived. Two requests
// racing to create the same customer both end with the one that won.
//
// Call it only with an address the user has proved they own: see
// CustomerByEmail.
func (c *Client) EnsureCustomer(ctx context.Context, email, name string) (*Customer, error) {
	cu, err := c.CreateCustomer(ctx, &CreateCustomerRequest{Email: email, Name: name})
	if err == nil {
		return cu, nil
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "customer_already_exists" {
		return nil, err
	}
	if cu, err = c.CustomerByEmail(ctx, email); err != nil {
		return nil, err
	}
	if cu.Status != CustomerArchived {
		return cu, nil
	}
	var out Customer
	body := map[string]string{"status": CustomerActive}
	if err := c.do(ctx, http.MethodPatch, "/customers/"+cu.ID, body, &out); err != nil {
		return nil, err
	}
	if err := expectID("customer", out.ID, cu.ID); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreatePortalSession creates temporary links into the customer portal,
// with direct links for the named subscriptions (at most 25).
func (c *Client) CreatePortalSession(ctx context.Context, customerID string, subscriptionIDs ...string) (*PortalSession, error) {
	if err := checkID("customer", customerID); err != nil {
		return nil, err
	}
	body := map[string]any{}
	if len(subscriptionIDs) > 0 {
		body["subscription_ids"] = subscriptionIDs
	}
	var out PortalSession
	if err := c.do(ctx, http.MethodPost, "/customers/"+customerID+"/portal-sessions", body, &out); err != nil {
		return nil, err
	}
	if err := expectID("portal session", out.ID, ""); err != nil {
		return nil, err
	}
	return &out, nil
}

// Page is one page of a list. After marks where the next page starts: pass
// it as the request's After. It is empty, and HasMore false, on the last
// page.
type Page[T any] struct {
	Items   []T
	After   string
	HasMore bool
}

// ListTransactionsRequest filters ListTransactions. Every field is optional.
type ListTransactionsRequest struct {
	// Status keeps the transactions in any of the statuses, such as
	// TransactionCompleted.
	Status []string

	// UpdatedFrom keeps the transactions updated at or after the time: the
	// window a reconciliation job walks.
	UpdatedFrom time.Time

	CustomerID     string
	SubscriptionID string

	// OrderBy is "<field>[ASC]" or "<field>[DESC]" with field one of id,
	// created_at, updated_at, billed_at; the API's default is id[DESC].
	OrderBy string

	// PerPage is at most 30, the API's limit and default.
	PerPage int

	// After is Page.After of the previous page.
	After string
}

// ListTransactions returns a page of transactions.
//
// It is how a lost webhook is found: Paddle stops retrying a delivery after
// three days, and a payment whose webhook never arrived would otherwise wait
// for the customer to complain. A periodic job that lists completed
// transactions updated since its last run, and applies each one the way the
// webhook would, closes that gap; the ledger's keys make a transaction
// applied twice a no-op.
func (c *Client) ListTransactions(ctx context.Context, req *ListTransactionsRequest) (*Page[Transaction], error) {
	if req == nil {
		req = &ListTransactionsRequest{}
	}
	q := url.Values{}
	if len(req.Status) > 0 {
		q.Set("status", strings.Join(req.Status, ","))
	}
	if !req.UpdatedFrom.IsZero() {
		q.Set("updated_at[GTE]", req.UpdatedFrom.UTC().Format(time.RFC3339))
	}
	for k, id := range map[string]string{"customer_id": req.CustomerID, "subscription_id": req.SubscriptionID, "after": req.After} {
		if id == "" {
			continue
		}
		if err := checkID(k, id); err != nil {
			return nil, err
		}
		q.Set(k, id)
	}
	if req.OrderBy != "" {
		q.Set("order_by", req.OrderBy)
	}
	if req.PerPage < 0 || req.PerPage > 30 {
		return nil, fmt.Errorf("%w: per page %d is outside 1..30", ErrInvalid, req.PerPage)
	}
	if req.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(req.PerPage))
	}
	return list(ctx, c, "/transactions", q, req.After, func(t Transaction) string { return t.ID })
}

// ListAdjustmentsRequest filters ListAdjustments. Every field is optional.
type ListAdjustmentsRequest struct {
	// Action keeps the adjustments with any of the actions, such as
	// ActionRefund and ActionChargeback.
	Action []string

	// Status keeps the adjustments in any of the statuses, such as
	// AdjustmentApproved.
	Status []string

	TransactionID  string
	CustomerID     string
	SubscriptionID string

	// PerPage is at most 50; the API's default is 10.
	PerPage int

	// After is Page.After of the previous page.
	After string
}

// ListAdjustments returns a page of adjustments, newest first: refunds and
// chargebacks for a reconciliation job, as ListTransactions finds payments.
func (c *Client) ListAdjustments(ctx context.Context, req *ListAdjustmentsRequest) (*Page[Adjustment], error) {
	if req == nil {
		req = &ListAdjustmentsRequest{}
	}
	q := url.Values{}
	if len(req.Action) > 0 {
		q.Set("action", strings.Join(req.Action, ","))
	}
	if len(req.Status) > 0 {
		q.Set("status", strings.Join(req.Status, ","))
	}
	for k, id := range map[string]string{
		"transaction_id": req.TransactionID, "customer_id": req.CustomerID,
		"subscription_id": req.SubscriptionID, "after": req.After,
	} {
		if id == "" {
			continue
		}
		if err := checkID(k, id); err != nil {
			return nil, err
		}
		q.Set(k, id)
	}
	if req.PerPage < 0 || req.PerPage > 50 {
		return nil, fmt.Errorf("%w: per page %d is outside 1..50", ErrInvalid, req.PerPage)
	}
	if req.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(req.PerPage))
	}
	return list(ctx, c, "/adjustments", q, req.After, func(a Adjustment) string { return a.ID })
}

// list fetches one page of a list endpoint. Where the next page starts is
// read from meta.pagination.next, a URL with an after parameter.
//
// A page that cannot be trusted is an error, not a short list: an item
// without an id, a promise of more pages with no usable place to continue,
// or a next page that starts where this one did. A reconciliation job that
// took any of these for the end would stop early and never know.
func list[T any](ctx context.Context, c *Client, path string, q url.Values, prev string, id func(T) string) (*Page[T], error) {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var items []T
	lr := &listResult{items: &items}
	if err := c.do(ctx, http.MethodGet, path, nil, lr); err != nil {
		return nil, err
	}
	for i, it := range items {
		if id(it) == "" {
			return nil, fmt.Errorf("%w: item %d of the list has no id", ErrResponse, i)
		}
	}
	p := &Page[T]{Items: items}
	if lr.page.Next != "" {
		if u, err := url.Parse(lr.page.Next); err == nil {
			p.After = u.Query().Get("after")
		}
	}
	switch {
	case lr.page.HasMore != nil && *lr.page.HasMore:
		if p.After == "" {
			return nil, fmt.Errorf("%w: has_more without a next page (next %q)", ErrResponse, lr.page.Next)
		}
		p.HasMore = true
	case lr.page.HasMore != nil:
		p.HasMore = false
	default:
		// Without has_more, a full page that names a next one may have it.
		p.HasMore = p.After != "" && len(items) > 0 && len(items) >= max(lr.page.PerPage, 1)
	}
	if p.HasMore && p.After == prev {
		return nil, fmt.Errorf("%w: the next page starts where this one did (after %q)", ErrResponse, prev)
	}
	if !p.HasMore {
		p.After = ""
	}
	return p, nil
}
