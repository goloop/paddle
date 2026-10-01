package paddle

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Money is an amount in the lowest denomination of its currency ("1000" is
// $10.00 in USD), as a string, exactly as the API sends it.
type Money struct {
	Amount       string `json:"amount"`
	CurrencyCode string `json:"currency_code"`
}

// Int64 returns the amount as an integer in the lowest denomination. See
// ParseAmount.
func (m Money) Int64() (int64, error) { return ParseAmount(m.Amount) }

// ParseAmount reads an amount as Paddle sends it - a string of digits in the
// lowest denomination of the currency, such as a Totals field - into an
// integer. It accepts nothing else: no decimal point, no exponent, no
// spaces, so an amount is never rounded through a float on its way to a
// balance.
func ParseAmount(s string) (int64, error) {
	digits := strings.TrimPrefix(s, "-")
	if digits == "" || strings.TrimLeft(digits, "0123456789") != "" {
		return 0, fmt.Errorf("paddle: amount %q is not an integer", s)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("paddle: amount %q: %w", s, err)
	}
	return n, nil
}

// Duration is a billing cycle or a trial period: every Frequency Intervals.
type Duration struct {
	// Interval is "day", "week", "month" or "year".
	Interval  string `json:"interval"`
	Frequency int    `json:"frequency"`
}

// QuantityRange limits how many of an item a checkout allows.
type QuantityRange struct {
	Minimum int `json:"minimum"`
	Maximum int `json:"maximum"`
}

// Period is a span of time, such as a subscription's current billing period.
type Period struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

// Price is a catalog or non-catalog price as the API returns it.
type Price struct {
	ID           string         `json:"id"`
	ProductID    string         `json:"product_id"`
	Description  string         `json:"description"`
	Name         string         `json:"name"`
	UnitPrice    Money          `json:"unit_price"`
	BillingCycle *Duration      `json:"billing_cycle"`
	TrialPeriod  *Duration      `json:"trial_period"`
	TaxMode      string         `json:"tax_mode"`
	Status       string         `json:"status"`
	CustomData   map[string]any `json:"custom_data"`
}

// Tax modes of a price.
const (
	TaxModeAccountSetting = "account_setting"
	TaxModeExternal       = "external"
	TaxModeInternal       = "internal"
)

// Transaction statuses.
const (
	TransactionDraft     = "draft"
	TransactionReady     = "ready"
	TransactionBilled    = "billed"
	TransactionPaid      = "paid"
	TransactionCompleted = "completed"
	TransactionCanceled  = "canceled"
	TransactionPastDue   = "past_due"
)

// Transaction is a Paddle transaction: a checkout, a renewal, an invoice.
type Transaction struct {
	ID             string              `json:"id"`
	Status         string              `json:"status"`
	CustomerID     string              `json:"customer_id"`
	AddressID      string              `json:"address_id"`
	BusinessID     string              `json:"business_id"`
	SubscriptionID string              `json:"subscription_id"`
	InvoiceID      string              `json:"invoice_id"`
	InvoiceNumber  string              `json:"invoice_number"`
	CurrencyCode   string              `json:"currency_code"`
	Origin         string              `json:"origin"`
	CollectionMode string              `json:"collection_mode"`
	BillingDetails *BillingDetails     `json:"billing_details"`
	CustomData     map[string]any      `json:"custom_data"`
	Items          []TransactionItem   `json:"items"`
	Details        *TransactionDetails `json:"details"`
	BillingPeriod  *Period             `json:"billing_period"`
	Checkout       *CheckoutLink       `json:"checkout"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	BilledAt       *time.Time          `json:"billed_at"`
}

// TransactionItem is one line of a transaction.
type TransactionItem struct {
	Price    Price `json:"price"`
	Quantity int   `json:"quantity"`
}

// TransactionDetails carries the calculated totals of a transaction and of
// each of its lines.
type TransactionDetails struct {
	Totals Totals `json:"totals"`

	// LineItems are the calculated lines. A partial refund names a line by
	// its ID (AdjustmentItem.ItemID).
	LineItems []TransactionLineItem `json:"line_items"`
}

// TransactionLineItem is one calculated line of a transaction.
type TransactionLineItem struct {
	// ID identifies the line ("txnitm_..."); it is what AdjustmentItem.ItemID
	// names.
	ID       string         `json:"id"`
	PriceID  string         `json:"price_id"`
	Quantity int            `json:"quantity"`
	Totals   LineItemTotals `json:"totals"`
}

// LineItemTotals are the calculated amounts of one line, in the lowest
// denomination of the transaction's currency.
type LineItemTotals struct {
	Subtotal string `json:"subtotal"`
	Discount string `json:"discount"`
	Tax      string `json:"tax"`
	Total    string `json:"total"`
}

// Totals are the calculated amounts of a transaction, in the lowest
// denomination of CurrencyCode.
type Totals struct {
	Subtotal     string `json:"subtotal"`
	Discount     string `json:"discount"`
	Tax          string `json:"tax"`
	Total        string `json:"total"`
	Credit       string `json:"credit"`
	Balance      string `json:"balance"`
	GrandTotal   string `json:"grand_total"`
	Fee          string `json:"fee"`
	Earnings     string `json:"earnings"`
	CurrencyCode string `json:"currency_code"`
}

// CheckoutLink is where a transaction is paid.
type CheckoutLink struct {
	// URL is the checkout page with the transaction id attached. It is set
	// when the account has a default payment link or the request named one.
	URL string `json:"url"`
}

// Subscription statuses.
const (
	SubscriptionActive   = "active"
	SubscriptionCanceled = "canceled"
	SubscriptionPastDue  = "past_due"
	SubscriptionPaused   = "paused"
	SubscriptionTrialing = "trialing"
)

// Subscription is a recurring billing agreement.
type Subscription struct {
	ID                   string             `json:"id"`
	Status               string             `json:"status"`
	CustomerID           string             `json:"customer_id"`
	AddressID            string             `json:"address_id"`
	BusinessID           string             `json:"business_id"`
	CurrencyCode         string             `json:"currency_code"`
	CollectionMode       string             `json:"collection_mode"`
	BillingCycle         Duration           `json:"billing_cycle"`
	CurrentBillingPeriod *Period            `json:"current_billing_period"`
	ScheduledChange      *ScheduledChange   `json:"scheduled_change"`
	Items                []SubscriptionItem `json:"items"`
	CustomData           map[string]any     `json:"custom_data"`
	ManagementURLs       *ManagementURLs    `json:"management_urls"`
	CreatedAt            time.Time          `json:"created_at"`
	UpdatedAt            time.Time          `json:"updated_at"`
	StartedAt            *time.Time         `json:"started_at"`
	FirstBilledAt        *time.Time         `json:"first_billed_at"`
	NextBilledAt         *time.Time         `json:"next_billed_at"`
	PausedAt             *time.Time         `json:"paused_at"`
	CanceledAt           *time.Time         `json:"canceled_at"`
}

// SubscriptionItem is one price a subscription bills.
type SubscriptionItem struct {
	Status   string `json:"status"`
	Quantity int    `json:"quantity"`
	Price    Price  `json:"price"`
}

// ScheduledChange is a change that takes effect later: a cancellation or a
// pause at the end of the period, or a resume.
type ScheduledChange struct {
	// Action is "cancel", "pause" or "resume".
	Action      string     `json:"action"`
	EffectiveAt time.Time  `json:"effective_at"`
	ResumeAt    *time.Time `json:"resume_at"`
}

// ManagementURLs are links the customer can use to manage a subscription.
type ManagementURLs struct {
	UpdatePaymentMethod string `json:"update_payment_method"`
	Cancel              string `json:"cancel"`
}

// Adjustment actions.
const (
	ActionRefund                   = "refund"
	ActionCredit                   = "credit"
	ActionChargeback               = "chargeback"
	ActionChargebackReverse        = "chargeback_reverse"
	ActionChargebackWarning        = "chargeback_warning"
	ActionChargebackWarningReverse = "chargeback_warning_reverse"
	ActionCreditReverse            = "credit_reverse"
)

// Adjustment statuses.
const (
	AdjustmentPendingApproval = "pending_approval"
	AdjustmentApproved        = "approved"
	AdjustmentRejected        = "rejected"
	AdjustmentReversed        = "reversed"
)

// Adjustment is a change to a billed or completed transaction: a refund, a
// credit, a chargeback and its reversal.
type Adjustment struct {
	ID             string           `json:"id"`
	Action         string           `json:"action"`
	Type           string           `json:"type"`
	TransactionID  string           `json:"transaction_id"`
	SubscriptionID string           `json:"subscription_id"`
	CustomerID     string           `json:"customer_id"`
	Reason         string           `json:"reason"`
	Status         string           `json:"status"`
	CurrencyCode   string           `json:"currency_code"`
	Items          []AdjustmentItem `json:"items"`
	Totals         *AdjustmentTotal `json:"totals"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      *time.Time       `json:"updated_at"`
}

// AdjustmentItem is the part of one transaction item an adjustment covers.
type AdjustmentItem struct {
	ID     string `json:"id,omitempty"`
	ItemID string `json:"item_id"`
	// Type is "full", "partial", "tax" or "proration".
	Type   string `json:"type"`
	Amount string `json:"amount,omitempty"`
}

// AdjustmentTotal is the calculated amount of an adjustment.
type AdjustmentTotal struct {
	Subtotal     string `json:"subtotal"`
	Tax          string `json:"tax"`
	Total        string `json:"total"`
	Fee          string `json:"fee"`
	Earnings     string `json:"earnings"`
	CurrencyCode string `json:"currency_code"`
}

// Customer statuses.
const (
	CustomerActive   = "active"
	CustomerArchived = "archived"
)

// Customer is a Paddle customer.
type Customer struct {
	ID         string         `json:"id"`
	Email      string         `json:"email"`
	Name       string         `json:"name"`
	Locale     string         `json:"locale"`
	Status     string         `json:"status"`
	CustomData map[string]any `json:"custom_data"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// PortalSession holds temporary links into the customer portal. They
// expire; create a session when the customer asks for it and never store
// the links.
type PortalSession struct {
	ID         string     `json:"id"`
	CustomerID string     `json:"customer_id"`
	URLs       PortalURLs `json:"urls"`
	CreatedAt  time.Time  `json:"created_at"`
}

// PortalURLs are the links of a portal session.
type PortalURLs struct {
	General struct {
		Overview string `json:"overview"`
	} `json:"general"`
	Subscriptions []PortalSubscriptionURLs `json:"subscriptions"`
}

// PortalSubscriptionURLs are the portal links for one subscription.
type PortalSubscriptionURLs struct {
	ID                              string `json:"id"`
	CancelSubscription              string `json:"cancel_subscription"`
	UpdateSubscriptionPaymentMethod string `json:"update_subscription_payment_method"`
}
