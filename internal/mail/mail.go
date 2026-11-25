// Package mail sends the app's two emails (sign-in link, draft ready).
//
// Emails are plain and short: a text part and a minimal HTML part with one
// button. No tracking pixels or tracked links are ever added; with Resend,
// open and click tracking stay off for the sending domain.
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Message is one email to one person.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer sends email.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// Simple builds a message with a short text and one button.
func Simple(to, subject, intro, buttonLabel, link, outro string) Message {
	text := intro + "\n\n" + buttonLabel + ": " + link + "\n"
	if outro != "" {
		text += "\n" + outro + "\n"
	}
	var b strings.Builder
	// Brand colours inline (email clients drop stylesheets); the name is set
	// as text, not an image, so nothing is loaded from the server.
	b.WriteString(`<!doctype html><html><body style="margin:0;background:#F5F2EC">`)
	b.WriteString(`<div style="font-family:'Instrument Sans',system-ui,-apple-system,'Segoe UI',sans-serif;font-size:16px;line-height:24px;color:#2A241F;max-width:480px;margin:0 auto;padding:32px 24px">`)
	b.WriteString(`<p lang="da" style="margin:0 0 24px;font-family:'Instrument Serif',Georgia,serif;font-size:24px;line-height:30px">Hvad skal vi <em>ha'?</em></p>`)
	fmt.Fprintf(&b, `<p style="margin:0 0 24px">%s</p>`, html.EscapeString(intro))
	fmt.Fprintf(&b, `<p style="margin:0 0 24px"><a href="%s" style="display:inline-block;background:#2A241F;color:#F5F2EC;text-decoration:none;font-weight:600;padding:14px 24px;border-radius:4px">%s</a></p>`,
		html.EscapeString(link), html.EscapeString(buttonLabel))
	if outro != "" {
		fmt.Fprintf(&b, `<p style="margin:0;color:#6B6157;font-size:14px;line-height:20px">%s</p>`, html.EscapeString(outro))
	}
	b.WriteString(`</div>`)
	b.WriteString(`</body></html>`)
	return Message{To: to, Subject: subject, Text: text, HTML: b.String()}
}

// Resend sends through the Resend HTTP API.
type Resend struct {
	APIKey string
	From   string // "Hvad skal vi ha'? <meals@liluhome.dk>"
	Client *http.Client
	URL    string // default https://api.resend.com/emails
}

// NewResend returns a Resend mailer with a 10 s timeout.
func NewResend(apiKey, from string) *Resend {
	return &Resend{APIKey: apiKey, From: from, Client: &http.Client{Timeout: 10 * time.Second}, URL: "https://api.resend.com/emails"}
}

func (r *Resend) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]any{
		"from": r.From, "to": []string{m.To}, "subject": m.Subject, "text": m.Text, "html": m.HTML,
	})
	if err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: send: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		return fmt.Errorf("mail: resend answered %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Log "sends" by logging the recipient and subject, never the body (it can
// hold a sign-in link). Used when RESEND_API_KEY is not set.
type Log struct{ Logger *slog.Logger }

func (l Log) Send(_ context.Context, m Message) error {
	log := l.Logger
	if log == nil {
		log = slog.Default()
	}
	log.Warn("email not sent: RESEND_API_KEY is not set", "subject", m.Subject)
	return nil
}

// Recorder keeps every message, for tests and the e2e server.
type Recorder struct {
	mu   sync.Mutex
	Sent []Message
	Err  error // returned by Send when set
}

func (r *Recorder) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.Sent = append(r.Sent, m)
	return nil
}

// To returns the messages sent to an address.
func (r *Recorder) To(addr string) []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Message
	for _, m := range r.Sent {
		if strings.EqualFold(m.To, addr) {
			out = append(out, m)
		}
	}
	return out
}

// Last returns the last message to addr.
func (r *Recorder) Last(addr string) (Message, error) {
	ms := r.To(addr)
	if len(ms) == 0 {
		return Message{}, errors.New("no message to " + addr)
	}
	return ms[len(ms)-1], nil
}
