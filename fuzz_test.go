package paddle

import (
	"testing"
	"time"
)

// FuzzWebhook feeds arbitrary headers and bodies to the verifier: it must
// never panic, and whatever it accepts must carry an event.
func FuzzWebhook(f *testing.F) {
	at := time.Unix(1893553445, 0)
	body := []byte(`{"event_id":"evt_1","event_type":"transaction.paid","data":{}}`)
	f.Add(Sign("s", body, at), body)
	f.Add("ts=1;h1=00;h1=zz;;=", []byte("{}"))
	f.Add("", []byte(nil))
	wh := NewWebhook("s", WithWebhookClock(func() time.Time { return at }))
	f.Fuzz(func(t *testing.T, header string, body []byte) {
		ev, err := wh.Parse(header, body)
		if err == nil && (ev == nil || ev.ID == "") {
			t.Fatal("accepted without an event")
		}
	})
}
