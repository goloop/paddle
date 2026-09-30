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
		switch ev.Type {
		case paddle.EventTransactionCompleted:
			tx, err := ev.Transaction()
			if err != nil {
				http.Error(w, "bad event", http.StatusBadRequest)
				return
			}
			_ = tx.CustomData["purchase"] // find the purchase and credit it, once per ev.ID
		case paddle.EventAdjustmentUpdated:
			// a refund or a chargeback was decided
		}
		w.WriteHeader(http.StatusOK)
	})
}

func ExampleSign() {
	body := []byte(`{"event_id":"evt_01","event_type":"transaction.completed","data":{}}`)
	at := time.Now()
	header := paddle.Sign("secret", body, at)

	ev, err := paddle.NewWebhook("secret").Parse(header, body)
	fmt.Println(ev.ID, err)
	// Output: evt_01 <nil>
}
