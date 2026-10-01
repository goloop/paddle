package paddle_test

import (
	"fmt"
	"net/http"
	"time"

	"github.com/goloop/paddle"
)

func ExampleNew() {
	c := paddle.New("pdl_sdbx_apikey_...")
	fmt.Println(c.Sandbox())
	// Output: true
}

// ExampleClient_CreateTransaction builds a transaction for an amount decided
// on the server. Sending it needs a real API key, so this example only shows
// the shape.
func ExampleClient_CreateTransaction() {
	req := &paddle.CreateTransactionRequest{
		Items: []paddle.TransactionItemRequest{{Price: &paddle.NonCatalogPrice{
			Description: "1000 credits",
			ProductID:   "pro_01",
			UnitPrice:   paddle.Money{Amount: "1000", CurrencyCode: "USD"},
		}}},
		CustomData: map[string]any{"purchase": "p_9"},
	}
	fmt.Println(req.Items[0].Price.UnitPrice.Amount, req.Items[0].Price.UnitPrice.CurrencyCode)
	// Output: 1000 USD
}

func ExampleWebhook() {
	wh := paddle.NewWebhook("pdl_ntfset_...")

	http.HandleFunc("POST /webhooks/paddle", func(w http.ResponseWriter, r *http.Request) {
		ev, err := wh.ParseRequest(r)
		if err != nil {
			http.Error(w, "bad signature", http.StatusBadRequest)
			return
		}
		if err := apply(ev); err != nil {
			http.Error(w, "retry", http.StatusInternalServerError) // Paddle sends it again
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// apply acts on an event. Every effect is keyed by the provider's id, so an
// event applied twice changes nothing.
func apply(ev *paddle.Event) error {
	switch ev.Type {
	case paddle.EventTransactionCompleted:
		tx, err := ev.Transaction()
		if err != nil {
			return err
		}
		_ = tx // check the amount and currency against the purchase, then credit it, keyed by tx.ID

	// A refund approved later sends adjustment.updated; a chargeback, and a
	// refund approved at once, send only adjustment.created.
	case paddle.EventAdjustmentCreated, paddle.EventAdjustmentUpdated:
		adj, err := ev.Adjustment()
		if err != nil {
			return err
		}
		if adj.Status != paddle.AdjustmentApproved {
			return nil // pending or rejected: nothing moved yet
		}
		switch adj.Action {
		case paddle.ActionRefund, paddle.ActionChargeback:
			// take back what the money bought, under the refund record's key
		case paddle.ActionChargebackReverse:
			// the dispute was won: give back what the chargeback took
		}
	}
	return nil
}

func ExampleSign() {
	body := []byte(`{"event_id":"evt_01","event_type":"transaction.completed","data":{}}`)
	at := time.Now()
	header := paddle.Sign("secret", body, at)

	ev, err := paddle.NewWebhook("secret").Parse(header, body)
	fmt.Println(ev.ID, err)
	// Output: evt_01 <nil>
}
