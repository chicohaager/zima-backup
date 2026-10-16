// Package dashboard puts a card on the ZimaOS home screen while scheduled
// jobs need the user — the only place a ZimaOS user looks every day
// without opening the module (measured on .143: three nightly backups had
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chicohaager/zima-backup/internal/engine"
)

// DefaultBusURLFile is where ZimaOS records the message bus address.
const DefaultBusURLFile = "/var/run/casaos/message-bus.url"

// CardID is stable, so a re-send replaces the card instead of stacking one.
const CardID = "zbackup:attention"

const icon = "/modules/zbackup/appicon.svg"

// Notifier posts and withdraws the card. The zero BusURLFile disables it.
type Notifier struct {
	BusURLFile string
	Client     *http.Client
}

type card struct {
	ID         string  `json:"id"`
	CardType   string  `json:"cardType"`
	RenderType string  `json:"renderType"`
	Content    content `json:"content"`
}

type content struct {
	TitleIcon        string     `json:"titleIcon"`
	TitleText        string     `json:"titleText"`
	BodyIconWithText *iconText  `json:"bodyIconWithText,omitempty"`
	FooterActions    []footerAc `json:"footerActions,omitempty"`
}

type iconText struct {
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

type footerAc struct {
	Side       string `json:"side"`
	Style      string `json:"style"`
	Text       string `json:"text"`
	MessageBus busAct `json:"messageBus"`
}

type busAct struct {
	Key     string `json:"key"`
	Payload string `json:"payload"`
}

// words are the card's sentences per UI language; {n} is a count, {d} a date.
type words struct {
	title, open            string
	one, many              string // "{n} …" lead-in
	failing, never, missed string
	more                   string
}

var texts = map[string]words{
	"en": {title: "Sync & Backup", open: "Open Sync & Backup",
		one: "1 scheduled job needs your attention:", many: "{n} scheduled jobs need your attention:",
		failing: "no successful run since {d}", never: "never completed", missed: "the run of {d} did not happen", more: "and {n} more"},
	"de": {title: "Sync & Backup", open: "Sync & Backup öffnen",
		one: "1 geplanter Job braucht deine Aufmerksamkeit:", many: "{n} geplante Jobs brauchen deine Aufmerksamkeit:",
		failing: "kein erfolgreicher Lauf seit {d}", never: "noch nie erfolgreich", missed: "der Lauf vom {d} hat nicht stattgefunden", more: "und {n} weitere"},
	"fr": {title: "Sync & Backup", open: "Ouvrir Sync & Backup",
		one: "1 tâche planifiée demande votre attention :", many: "{n} tâches planifiées demandent votre attention :",
		failing: "aucune exécution réussie depuis le {d}", never: "jamais terminée", missed: "l’exécution du {d} n’a pas eu lieu", more: "et {n} de plus"},
	"es": {title: "Sync & Backup", open: "Abrir Sync & Backup",
		one: "1 tarea programada necesita tu atención:", many: "{n} tareas programadas necesitan tu atención:",
		failing: "ninguna ejecución correcta desde el {d}", never: "nunca completada", missed: "la ejecución del {d} no se realizó", more: "y {n} más"},
	"zh": {title: "Sync & Backup", open: "打开 Sync & Backup",
		one: "1 个计划任务需要您处理：", many: "{n} 个计划任务需要您处理：",
		failing: "自 {d} 起没有成功运行", never: "从未成功完成", missed: "{d} 的运行未执行", more: "另有 {n} 个"},
}

// shown is how many jobs the card names before "and N more".
const shown = 3

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
// language is unknown).
func Text(lang string, list []engine.Attention) string {
	w, ok := texts[lang]
	if !ok {
		lang, w = "en", texts["en"]
	}
	lead := w.one
	if len(list) != 1 {
		lead = strings.ReplaceAll(w.many, "{n}", fmt.Sprint(len(list)))
	}
	var parts []string
	for i, a := range list {
		if i == shown {
			parts = append(parts, strings.ReplaceAll(w.more, "{n}", fmt.Sprint(len(list)-shown)))
			break
		}
		why := w.never
		switch {
		case a.Reason == engine.AttentionOverdue:
			why = strings.ReplaceAll(w.missed, "{d}", date(lang, a.Since))
		case !a.LastSuccess.IsZero():
			why = strings.ReplaceAll(w.failing, "{d}", date(lang, a.LastSuccess))
		}
		parts = append(parts, a.Name+" ("+why+")")
	}
	return lead + " " + strings.Join(parts, ", ")
}

func build(lang string, list []engine.Attention) card {
	w, ok := texts[lang]
	if !ok {
		w = texts["en"]
	}
	open, _ := json.Marshal(map[string]string{"url": "modules/zbackup/index.html"})
	return card{
		ID: CardID, CardType: "long-notice", RenderType: "icon-text-notice",
		Content: content{
			TitleIcon:        icon,
			TitleText:        w.title,
			BodyIconWithText: &iconText{Icon: icon, Description: Text(lang, list)},
			FooterActions: []footerAc{{Side: "right", Style: "primary", Text: w.open,
				MessageBus: busAct{Key: "casaos-ui:open_files", Payload: string(open)}}},
		},
	}
}

func (n Notifier) busURL() (string, error) {
	b, err := os.ReadFile(n.BusURLFile)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(string(b)))
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		// the bus is a local service; never follow the file anywhere else
		return "", fmt.Errorf("message bus url %q is not a loopback http url", strings.TrimSpace(string(b)))
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (n Notifier) publish(ctx context.Context, event string, props map[string]string) error {
	if n.BusURLFile == "" {
		return nil
	}
	base, err := n.busURL()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(props)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v2/message_bus/event/ysk/"+url.PathEscape(event), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("message bus %s: HTTP %d", event, resp.StatusCode)
	}
	return nil
}

// Show sends (or re-sends) the card.
func (n Notifier) Show(ctx context.Context, lang string, list []engine.Attention) error {
	b, err := json.Marshal(build(lang, list))
	if err != nil {
		return err
	}
	return n.publish(ctx, "ysk:card:upsert", map[string]string{"card:body": string(b)})
}

// Withdraw removes the card from the dashboards that are open right now.
func (n Notifier) Withdraw(ctx context.Context) error {
	return n.publish(ctx, "ysk:card:delete", map[string]string{"card:id": CardID})
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
		}
	case k.shown:
		if err = k.Notifier.Withdraw(ctx); err == nil {
			k.shown = false
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
