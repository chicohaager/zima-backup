// Package dashboard puts a card on the ZimaOS home screen while scheduled
// jobs need the user — the only place a ZimaOS user looks every day
// without opening the module (measured on the test NAS: three nightly backups had
// failed for twelve days, the module's own page was the only sign).
//
// Cards ("You should know", YSK) go through the ZimaOS message bus.
// Measured on 1.8.0-beta2 (KB §75) and used the same way by ZFW:
//   - a card is delivered live to the dashboards open at that moment and is
//     not stored — a reload drops it, GET /ysk stays empty. The card is
//     therefore re-sent while the trouble lasts (Keeper);
//   - a footer button can only open a URL (key "casaos-ui:open_files",
//     window.open(origin + "/" + payload.url)).
package dashboard

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/chicohaager/lintux-modkit/ysk"
	"github.com/chicohaager/zima-backup/internal/engine"
)

// DefaultBusURLFile is where ZimaOS records the message bus address.
const DefaultBusURLFile = ysk.DefaultBusURLFile

// CardID is stable, so a re-send replaces the card instead of stacking one.
const CardID = "zbackup:attention"

const icon = "/modules/zbackup/appicon.svg"

// card is the YSK card (lintux-modkit ysk, shared with the Module Manager).
type card = ysk.Card

// words are the card's sentences per UI language; {n} is a count, {d} a date.
type words struct {
	title, open            string
	one, many              string // "{n} …" lead-in; many carries the oldest trouble date
	failing, never, missed string
}

var texts = map[string]words{
	"en": {title: "Sync & Backup", open: "Open Sync & Backup",
		one: "1 scheduled job needs your attention:", many: "{n} scheduled jobs need attention, trouble since {d}:",
		failing: "no successful run since {d}", never: "never completed", missed: "the run of {d} did not happen"},
	"de": {title: "Sync & Backup", open: "Sync & Backup öffnen",
		one: "1 geplanter Job braucht deine Aufmerksamkeit:", many: "{n} geplante Jobs brauchen Aufmerksamkeit, Probleme seit {d}:",
		failing: "kein erfolgreicher Lauf seit {d}", never: "noch nie erfolgreich", missed: "der Lauf vom {d} hat nicht stattgefunden"},
	"fr": {title: "Sync & Backup", open: "Ouvrir Sync & Backup",
		one: "1 tâche planifiée demande votre attention :", many: "{n} tâches planifiées en échec depuis le {d} :",
		failing: "aucune exécution réussie depuis le {d}", never: "jamais terminée", missed: "l’exécution du {d} n’a pas eu lieu"},
	"es": {title: "Sync & Backup", open: "Abrir Sync & Backup",
		one: "1 tarea programada necesita tu atención:", many: "{n} tareas programadas con problemas desde el {d}:",
		failing: "ninguna ejecución correcta desde el {d}", never: "nunca completada", missed: "la ejecución del {d} no se realizó"},
	"zh": {title: "Sync & Backup", open: "打开 Sync & Backup",
		one: "1 个计划任务需要您处理：", many: "{n} 个计划任务自 {d} 起出现问题：",
		failing: "自 {d} 起没有成功运行", never: "从未成功完成", missed: "{d} 的运行未执行"},
}

// budget is how wide the card body may get. ZimaOS cuts the body after
// four lines: on the test NAS (1.8.0, window 1400 px) 156 characters were visible,
// the rest of the third job name was gone. A CJK character counts twice.
const budget = 140

func width(s string) int {
	w := 0
	for _, r := range s {
		w++
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			w++
		}
	}
	return w
}

// cut shortens s to at most w columns, ending in "…".
func cut(s string, w int) string {
	if width(s) <= w {
		return s
	}
	rs := []rune(s)
	for len(rs) > 0 && width(string(rs))+1 > w {
		rs = rs[:len(rs)-1]
	}
	return strings.TrimSpace(string(rs)) + "…"
}

func date(lang string, t time.Time) string {
	t = t.Local()
	switch lang {
	case "de":
		return t.Format("02.01.")
	case "fr", "es":
		return t.Format("02/01")
	case "zh":
		return fmt.Sprintf("%d月%d日", t.Month(), t.Day())
	default:
		return t.Format("Jan 2")
	}
}

// Text is the card's body for the given UI language (English when the
// language is unknown). One job is named with its reason; several are
// named by name only, behind the date their trouble began, and those that
// do not fit are counted ("+2") — the card shows four lines at most.
func Text(lang string, list []engine.Attention) string {
	w, ok := texts[lang]
	if !ok {
		lang, w = "en", texts["en"]
	}
	if len(list) == 1 {
		a := list[0]
		why := w.never
		switch {
		case a.Reason == engine.AttentionOverdue:
			why = strings.ReplaceAll(w.missed, "{d}", date(lang, a.Since))
		case !a.LastSuccess.IsZero():
			why = strings.ReplaceAll(w.failing, "{d}", date(lang, a.LastSuccess))
		}
		room := budget - width(w.one) - width(why) - 4 // spaces and brackets
		return w.one + " " + cut(a.Name, max(room, 8)) + " (" + why + ")"
	}
	oldest := list[0].Since // Attention sorts oldest trouble first
	for _, a := range list {
		if !a.Since.IsZero() && (oldest.IsZero() || a.Since.Before(oldest)) {
			oldest = a.Since
		}
	}
	lead := strings.ReplaceAll(strings.ReplaceAll(w.many, "{n}", fmt.Sprint(len(list))), "{d}", date(lang, oldest))
	out := lead
	for i, a := range list {
		sep := " "
		if i > 0 {
			sep = ", "
		}
		more := ""
		if rest := len(list) - i - 1; rest > 0 {
			more = fmt.Sprintf(", +%d", rest) // room kept for the count
		}
		if width(out+sep+a.Name+more) > budget {
			if i == 0 { // not even one name fits whole
				return out + sep + cut(a.Name, budget-width(out+sep+more)) + more
			}
			return out + fmt.Sprintf(" +%d", len(list)-i)
		}
		out += sep + a.Name
	}
	return out
}

func build(lang string, list []engine.Attention) card {
	w, ok := texts[lang]
	if !ok {
		w = texts["en"]
	}
	return ysk.NewCard(CardID, w.title, icon, Text(lang, list), w.open, "modules/zbackup/index.html")
}

// Notifier posts and withdraws the card. The zero BusURLFile disables it.
type Notifier struct {
	BusURLFile string
	Client     *http.Client
}

func (n Notifier) bus() ysk.Notifier { return ysk.Notifier{BusURLFile: n.BusURLFile, Client: n.Client} }

// Show sends (or re-sends) the card.
func (n Notifier) Show(ctx context.Context, lang string, list []engine.Attention) error {
	return n.bus().Show(ctx, build(lang, list))
}

// Withdraw removes the card from the dashboards that are open right now.
func (n Notifier) Withdraw(ctx context.Context) error {
	return n.bus().Withdraw(ctx, CardID)
}

// Keeper re-sends the card every Every while Attention reports jobs, and
// withdraws it once when the list becomes empty. Errors are logged when
// they change, not on every round.
type Keeper struct {
	Notifier  Notifier
	Attention func() []engine.Attention
	Lang      func() string
	Every     time.Duration

	shown   bool
	started bool
	lastErr string
	sent    string // the job names of the card last sent, for the journal
}

// Run loops until ctx ends.
func (k *Keeper) Run(ctx context.Context) {
	t := time.NewTicker(k.Every)
	defer t.Stop()
	for {
		k.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Round is one look: show, withdraw or nothing.
func (k *Keeper) Round(ctx context.Context) {
	if !k.started {
		// a card of the previous daemon may still sit in an open dashboard
		k.started, k.shown = true, true
	}
	list := k.Attention()
	var err error
	switch {
	case len(list) > 0:
		if err = k.Notifier.Show(ctx, k.Lang(), list); err == nil {
			k.shown = true
			names := make([]string, len(list))
			for i, a := range list {
				names[i] = a.Name
			}
			if s := strings.Join(names, ", "); s != k.sent {
				noun := "jobs"
				if len(list) == 1 {
					noun = "job"
				}
				log.Printf("[zbackup] dashboard card sent: %d %s (%s)", len(list), noun, s)
				k.sent = s
			}
		}
	case k.shown:
		if err = k.Notifier.Withdraw(ctx); err == nil {
			k.shown = false
			if k.sent != "" {
				log.Printf("[zbackup] dashboard card withdrawn")
				k.sent = ""
			}
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != k.lastErr {
		if msg != "" {
			log.Printf("[zbackup] dashboard card: %s", msg)
		} else {
			log.Printf("[zbackup] dashboard card: delivered again")
		}
		k.lastErr = msg
	}
}
