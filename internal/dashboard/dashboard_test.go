package dashboard

import (
	"context"
	"encoding/json"
	"log"
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
	}
	if got, want := Text("de", list), "3 geplante Jobs brauchen Aufmerksamkeit, Probleme seit 05.10.: BYD, Bücher, nightly"; got != want {
		t.Errorf("de:\n got %s\nwant %s", got, want)
	}
	if got, want := Text("xx", list[:1]), "1 scheduled job needs your attention: BYD (no successful run since Oct 4)"; got != want {
		t.Errorf("unknown language:\n got %s\nwant %s", got, want)
	}
	for lang, w := range texts {
		for name, s := range map[string]string{"title": w.title, "open": w.open, "one": w.one, "many": w.many, "failing": w.failing, "never": w.never, "missed": w.missed} {
			if s == "" {
				t.Errorf("%s: %s is empty", lang, name)
			}
		}
	}
	if len(texts) != 5 {
		t.Errorf("%d languages, the UI has 5", len(texts))
	}
}

// ZimaOS cuts the card body after four lines; on the test NAS (1.8.0, window
// 1400 px) 156 characters were visible. The test NAS's card of 16.10.2026 named
// three jobs with a date each and lost the end of the third name.
func TestTextFitsTheCard(t *testing.T) {
	const visible = 156 // measured, independent of the code's budget
	long := []engine.Attention{
		{Name: "BYD → HDD-Storage", Reason: engine.AttentionFailing, LastSuccess: oct(4, 3), Since: oct(5, 3)},
		{Name: "Bücher → sda", Reason: engine.AttentionFailing, LastSuccess: oct(4, 3), Since: oct(5, 3)},
		{Name: "Linux-Client → HDD-Storage", Reason: engine.AttentionFailing, LastSuccess: oct(4, 3), Since: oct(5, 3)},
		{Name: "Familienfotos vom Handy → Cube Pro über Tailscale", Reason: engine.AttentionFailing, Since: oct(6, 3)},
		{Name: "Dokumente", Reason: engine.AttentionOverdue, Since: oct(7, 3)},
	}
	for lang := range texts {
		for n := 1; n <= len(long); n++ {
			got := Text(lang, long[:n])
			if w := width(got); w > visible {
				t.Errorf("%s, %d jobs: width %d > %d: %s", lang, n, w, visible, got)
			}
		}
	}
	if got, want := Text("en", long[:3]), "3 scheduled jobs need attention, trouble since Oct 5: BYD → HDD-Storage, Bücher → sda, Linux-Client → HDD-Storage"; got != want {
		t.Errorf("three jobs that fit are all named:\n got %s\nwant %s", got, want)
	}
	if got := Text("en", long); !strings.HasSuffix(got, "+2") && !strings.HasSuffix(got, "+1") {
		t.Errorf("jobs that do not fit are counted: %s", got)
	}
	one := []engine.Attention{{Name: strings.Repeat("sehr langer Jobname ", 10), Reason: engine.AttentionFailing, LastSuccess: oct(4, 3)}}
	if got := Text("de", one); width(got) > visible || !strings.Contains(got, "…") || !strings.HasSuffix(got, "(kein erfolgreicher Lauf seit 04.10.)") {
		t.Errorf("a long single name is cut, the reason stays: %s", got)
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

// The journal says when the card went out and for which jobs, and when it
// was withdrawn: on 16.10.2026 a card did not show within 165 s once and
// nothing on the box said whether it had been sent.
func TestKeeperLogsWhenTheCardGoesOutAndComesBack(t *testing.T) {
	var buf strings.Builder
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	_, file := newBus(t)
	trouble := []engine.Attention{{Name: "BYD", Reason: engine.AttentionFailing, LastSuccess: oct(4, 3)}}
	k := &Keeper{Notifier: Notifier{BusURLFile: file}, Attention: func() []engine.Attention { return trouble }, Lang: func() string { return "en" }}
	ctx := context.Background()
	k.Round(ctx)
	k.Round(ctx) // same jobs: no new line
	trouble = append(trouble, engine.Attention{Name: "Bücher", Reason: engine.AttentionOverdue, Since: oct(5, 3)})
	k.Round(ctx)
	trouble = nil
	k.Round(ctx)
	k.Round(ctx)
	got := buf.String()
	for _, want := range []string{"dashboard card sent: 1 job (BYD)", "dashboard card sent: 2 jobs (BYD, Bücher)", "dashboard card withdrawn"} {
		if strings.Count(got, want) != 1 {
			t.Errorf("want %q once in the log:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "dashboard card sent"); n != 2 {
		t.Errorf("%d send lines, want 2:\n%s", n, got)
	}
}

// A start without trouble withdraws a card a previous daemon may have left,
// but says nothing: it did not send one.
func TestKeeperStartWithoutTroubleWritesNoJournalLine(t *testing.T) {
	var buf strings.Builder
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	b, file := newBus(t)
	k := &Keeper{Notifier: Notifier{BusURLFile: file}, Attention: func() []engine.Attention { return nil }, Lang: func() string { return "en" }}
	k.Round(context.Background())
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.events) != 1 || b.events[0] != "ysk:card:delete" {
		t.Fatalf("events %v", b.events)
	}
	if strings.Contains(buf.String(), "dashboard card") {
		t.Errorf("journal: %s", buf.String())
	}
}
