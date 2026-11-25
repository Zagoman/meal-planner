// Package fetch reads a recipe web page for the importer: only public
// addresses (never the home network), at most 2 MB in 10 s, reduced to
// plain text. A page with schema.org Recipe data (JSON-LD) is reduced to
// that data.
package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html"
)

// Limits from the handoff.
const (
	Timeout  = 10 * time.Second
	MaxBytes = 2 << 20
	// MaxText caps the text sent to Claude.
	MaxText = 30000
)

// ErrBlocked is returned for an address that is not public.
var ErrBlocked = errors.New("fetch: that address is not on the public internet")

// Fetcher gets pages. The zero value is ready to use.
type Fetcher struct {
	// AllowPrivate turns the public-address check off (tests only).
	AllowPrivate bool
}

// blocked prefixes besides what netip classifies: shared address space
// (CGNAT, Tailscale), benchmarking, IETF protocol assignments, TEST-NETs,
// 6to4 relay, reserved.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// Public reports whether an address may be fetched.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func (f Fetcher) client() *http.Client {
	dialer := &net.Dialer{
		Timeout: Timeout,
		// Checked on the address actually connected to, after DNS and on
		// every redirect, so a name that resolves to the LAN is refused too.
		Control: func(_, address string, _ syscall.RawConn) error {
			if f.AllowPrivate {
				return nil
			}
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !Public(ap.Addr()) {
				return ErrBlocked
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: Timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   Timeout,
			ResponseHeaderTimeout: Timeout,
			MaxIdleConns:          1,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("fetch: too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("fetch: only http and https links")
			}
			return nil
		},
	}
}

// CheckURL validates a link before it is fetched.
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("fetch: not an http or https link")
	}
	return u, nil
}

// Text fetches a page and returns its recipe as plain text.
func (f Fetcher) Text(ctx context.Context, raw string) (string, error) {
	u, err := CheckURL(raw)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	req.Header.Set("User-Agent", "MealPlannerRecipeImport/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9")
	res, err := f.client().Do(req)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			return "", ErrBlocked
		}
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("fetch: the page answered %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	if len(body) > MaxBytes {
		return "", errors.New("fetch: the page is larger than 2 MB")
	}
	ct := strings.ToLower(res.Header.Get("Content-Type"))
	var text string
	switch {
	case strings.HasPrefix(ct, "text/plain"):
		text = string(body)
	case ct == "" || strings.Contains(ct, "html"):
		text = PageText(body)
	default:
		return "", fmt.Errorf("fetch: not a web page (%s)", ct)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("fetch: the page has no text")
	}
	if len(text) > MaxText {
		text = text[:MaxText]
	}
	return text, nil
}

// PageText reduces an HTML page to text: its schema.org Recipe data if it
// has any, otherwise the visible text without scripts, styles, navigation,
// headers and footers.
func PageText(page []byte) string {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return ""
	}
	var ld []string
	var b strings.Builder
	skip := map[string]bool{"script": true, "style": true, "noscript": true, "nav": true, "header": true, "footer": true, "svg": true, "form": true, "aside": true}
	block := map[string]bool{"p": true, "div": true, "li": true, "br": true, "h1": true, "h2": true, "h3": true, "h4": true, "tr": true, "section": true, "article": true}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			for _, a := range n.Attr {
				if a.Key == "type" && strings.Contains(a.Val, "ld+json") && n.FirstChild != nil {
					ld = append(ld, n.FirstChild.Data)
				}
			}
		}
		if n.Type == html.ElementNode && skip[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				b.WriteString(t)
				b.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && block[n.Data] {
			b.WriteByte('\n')
		}
	}
	walk(doc)
	for _, raw := range ld {
		if t := recipeFromJSONLD(raw); t != "" {
			return t
		}
	}
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// recipeFromJSONLD finds a schema.org Recipe in a JSON-LD block (alone, in
// a list, or in @graph) and writes it as text.
func recipeFromJSONLD(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return ""
	}
	r := findRecipe(v)
	if r == nil {
		return ""
	}
	var b strings.Builder
	line := func(label string, val any) {
		if s := flatten(val); s != "" {
			fmt.Fprintf(&b, "%s: %s\n", label, s)
		}
	}
	line("Title", r["name"])
	line("Description", r["description"])
	line("Serves", r["recipeYield"])
	line("Total time", r["totalTime"])
	line("Category", r["recipeCategory"])
	if ings, ok := r["recipeIngredient"].([]any); ok {
		b.WriteString("Ingredients:\n")
		for _, i := range ings {
			fmt.Fprintf(&b, "- %s\n", flatten(i))
		}
	}
	b.WriteString("Steps:\n")
	for i, st := range steps(r["recipeInstructions"]) {
		fmt.Fprintf(&b, "%d. %s\n", i+1, st)
	}
	return b.String()
}

func findRecipe(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		if isRecipe(t["@type"]) {
			return t
		}
		if g, ok := t["@graph"]; ok {
			return findRecipe(g)
		}
	case []any:
		for _, x := range t {
			if r := findRecipe(x); r != nil {
				return r
			}
		}
	}
	return nil
}

func isRecipe(t any) bool {
	switch x := t.(type) {
	case string:
		return x == "Recipe"
	case []any:
		for _, y := range x {
			if s, ok := y.(string); ok && s == "Recipe" {
				return true
			}
		}
	}
	return false
}

func steps(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		for _, l := range strings.Split(t, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
	case []any:
		for _, x := range t {
			if m, ok := x.(map[string]any); ok && m["itemListElement"] != nil {
				out = append(out, steps(m["itemListElement"])...)
				continue
			}
			if s := flatten(x); s != "" {
				out = append(out, s)
			}
		}
	case map[string]any:
		if s := flatten(t); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func flatten(v any) string {
	switch t := v.(type) {
	case string:
		return strings.Join(strings.Fields(html.UnescapeString(t)), " ")
	case float64:
		return fmt.Sprint(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			if s := flatten(x); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		for _, k := range []string{"text", "name"} {
			if s := flatten(t[k]); s != "" {
				return s
			}
		}
	}
	return ""
}
