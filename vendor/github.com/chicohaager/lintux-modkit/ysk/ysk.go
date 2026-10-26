// Package ysk puts a card on the ZimaOS dashboard ("You should know", YSK)
// through the ZimaOS message bus — the one place a ZimaOS user looks every
// day without opening a module. Used by Sync & Backup (jobs that need the
// user) and the Module Manager (module updates).
//
// Measured on ZimaOS 1.8.0 (ZIMAOS-KNOWLEDGE §75):
//   - a card reaches only the dashboards open at that moment and is not
//     stored (a reload drops it, GET /ysk stays empty): the module re-sends it
//     while the reason lasts;
//   - a footer button can only open a URL (key "casaos-ui:open_files",
//     window.open(origin + "/" + payload.url));
//   - the body is cut after four lines (156 characters were visible at 1400 px).
package ysk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultBusURLFile is where ZimaOS records the message bus address.
const DefaultBusURLFile = "/var/run/casaos/message-bus.url"

// Card is the YSK card in the form the dashboard renders (CasaOS-MessageBus
// openapi.yaml, YSKCard).
type Card struct {
	ID         string  `json:"id"`
	CardType   string  `json:"cardType"`
	RenderType string  `json:"renderType"`
	Content    Content `json:"content"`
}

// Content of a card.
type Content struct {
	TitleIcon        string         `json:"titleIcon"`
	TitleText        string         `json:"titleText"`
	BodyIconWithText *IconText      `json:"bodyIconWithText,omitempty"`
	FooterActions    []FooterAction `json:"footerActions,omitempty"`
}

// IconText is the card body.
type IconText struct {
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

// FooterAction is a button.
type FooterAction struct {
	Side       string    `json:"side"`
	Style      string    `json:"style"`
	Text       string    `json:"text"`
	MessageBus BusAction `json:"messageBus"`
}

// BusAction is what a button does.
type BusAction struct {
	Key     string `json:"key"`
	Payload string `json:"payload"`
}

// NewCard is a card with a title, a body text and one button that opens
// openURL (relative to the dashboard, e.g. "modules/zbackup/index.html").
// A stable id makes a re-send replace the card instead of stacking one.
func NewCard(id, title, icon, text, button, openURL string) Card {
	open, _ := json.Marshal(map[string]string{"url": openURL})
	return Card{
		ID: id, CardType: "long-notice", RenderType: "icon-text-notice",
		Content: Content{
			TitleIcon:        icon,
			TitleText:        title,
			BodyIconWithText: &IconText{Icon: icon, Description: text},
			FooterActions: []FooterAction{{Side: "right", Style: "primary", Text: button,
				MessageBus: BusAction{Key: "casaos-ui:open_files", Payload: string(open)}}},
		},
	}
}

// Notifier posts and withdraws cards. The zero BusURLFile disables it.
type Notifier struct {
	BusURLFile string
	Client     *http.Client
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
func (n Notifier) Show(ctx context.Context, c Card) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return n.publish(ctx, "ysk:card:upsert", map[string]string{"card:body": string(b)})
}

// Withdraw removes the card with this id from the dashboards open right now.
func (n Notifier) Withdraw(ctx context.Context, id string) error {
	return n.publish(ctx, "ysk:card:delete", map[string]string{"card:id": id})
}
