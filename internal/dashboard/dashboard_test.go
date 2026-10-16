package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chicohaager/zima-backup/internal/engine"
)

type bus struct {
	mu     sync.Mutex
	events []string
	bodies []map[string]string
}

func newBus(t *testing.T) (*bus, string) {
	t.Helper()
	b := &bus{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var props map[string]string
		_ = json.NewDecoder(r.Body).Decode(&props)
		b.mu.Lock()
		b.events = append(b.events, strings.TrimPrefix(r.URL.Path, "/v2/message_bus/event/ysk/"))
		b.bodies = append(b.bodies, props)
		b.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	f := filepath.Join(t.TempDir(), "message-bus.url")
	if err := os.WriteFile(f, []byte(srv.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return b, f
}

var oct = func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, time.Local) }

func TestTextNamesJobsAndDatesInTheUILanguage(t *testing.T) {
	list := []engine.Attention{
		{Name: "BYD", Reason: engine.AttentionFailing, LastSuccess: oct(4, 3), Since: oct(5, 3)},
		{Name: "Bücher", Reason: engine.AttentionFailing, Since: oct(5, 3)},
		{Name: "nightly", Reason: engine.AttentionOverdue, Since: oct(15, 3)},
		{Name: "four", Reason: engine.AttentionFailing, LastSuccess: oct(1, 3)},
		{Name: "five", Reason: engine.AttentionFailing, LastSuccess: oct(1, 3)},
	}
	if got, want := Text("de", list), "5 geplante Jobs brauchen deine Aufmerksamkeit: BYD (kein erfolgreicher Lauf seit 04.10.), Bücher (noch nie erfolgreich), nightly (der Lauf vom 15.10. hat nicht stattgefunden), und 2 weitere"; got != want {
		t.Errorf("de:\n got %s\nwant %s", got, want)
	}
	if got, want := Text("xx", list[:1]), "1 scheduled job needs your attention: BYD (no successful run since Oct 4)"; got != want {
		t.Errorf("unknown language:\n got %s\nwant %s", got, want)
	}
	for lang, w := range texts {
		for name, s := range map[string]string{"title": w.title, "open": w.open, "one": w.one, "many": w.many, "failing": w.failing, "never": w.never, "missed": w.missed, "more": w.more} {
			if s == "" {
				t.Errorf("%s: %s is empty", lang, name)
			}
		}
	}
	if len(texts) != 5 {
		t.Errorf("%d languages, the UI has 5", len(texts))
	}
}

func TestKeeperShowsWhileTroubleLastsAndWithdrawsOnce(t *testing.T) {
	b, file := newBus(t)
	trouble := []engine.Attention{{Name: "BYD", Reason: engine.AttentionFailing, LastSuccess: oct(4, 3)}}
	k := &Keeper{Notifier: Notifier{BusURLFile: file}, Attention: func() []engine.Attention { return trouble }, Lang: func() string { return "de" }}
	ctx := context.Background()
	k.Round(ctx)
	k.Round(ctx) // re-sent: the dashboard does not keep it
	trouble = nil
	k.Round(ctx)
	k.Round(ctx) // nothing left to withdraw
	b.mu.Lock()
	defer b.mu.Unlock()
	if got := strings.Join(b.events, " "); got != "ysk:card:upsert ysk:card:upsert ysk:card:delete" {
		t.Fatalf("events: %s", got)
	}
	var c card
	if err := json.Unmarshal([]byte(b.bodies[0]["card:body"]), &c); err != nil {
		t.Fatal(err)
	}
	if c.ID != CardID || c.Content.TitleText != "Sync & Backup" || !strings.Contains(c.Content.BodyIconWithText.Description, "BYD (kein erfolgreicher Lauf seit 04.10.)") {
		t.Errorf("card %+v", c)
	}
	if a := c.Content.FooterActions; len(a) != 1 || a[0].MessageBus.Key != "casaos-ui:open_files" || a[0].MessageBus.Payload != `{"url":"modules/zbackup/index.html"}` {
		t.Errorf("button %+v", a)
	}
	if b.bodies[2]["card:id"] != CardID {
		t.Errorf("delete body %v", b.bodies[2])
	}
}

func TestKeeperClearsACardLeftByThePreviousDaemon(t *testing.T) {
	b, file := newBus(t)
	k := &Keeper{Notifier: Notifier{BusURLFile: file}, Attention: func() []engine.Attention { return nil }, Lang: func() string { return "en" }}
	k.Round(context.Background())
	k.Round(context.Background())
	if got := strings.Join(b.events, " "); got != "ysk:card:delete" {
		t.Fatalf("events: %q", got)
	}
}

func TestNotifierRefusesABusOffTheBox(t *testing.T) {
	f := filepath.Join(t.TempDir(), "u")
	_ = os.WriteFile(f, []byte("http://192.0.2.7:37065"), 0o600)
	if err := (Notifier{BusURLFile: f}).Withdraw(context.Background()); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v", err)
	}
}
