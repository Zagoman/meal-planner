package fetch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/fetch"
)

func TestPublic(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"93.184.215.14": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.0.0.5": false, "192.168.0.31": false, "172.16.3.4": false,
		"169.254.169.254": false, "100.100.100.100": false, "0.0.0.0": false, "::1": false,
		"fd12::1": false, "fe80::1": false, "::ffff:192.168.0.31": false, "224.0.0.1": false,
	} {
		if got := fetch.Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %v", addr, got)
		}
	}
}

func TestBlocksTheHomeNetwork(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret router page"))
	}))
	defer srv.Close()
	// httptest listens on 127.0.0.1: refused.
	if _, err := (fetch.Fetcher{}).Text(context.Background(), srv.URL); !errors.Is(err, fetch.ErrBlocked) {
		t.Fatalf("loopback fetched: %v", err)
	}
	for _, bad := range []string{"file:///etc/passwd", "ftp://example.com/x", "http://user:pw@example.com/", "not a url"} {
		if _, err := (fetch.Fetcher{}).Text(context.Background(), bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestPageTextAndJSONLD(t *testing.T) {
	t.Parallel()
	ld := `<html><head><script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebPage"},
	  {"@type":"Recipe","name":"Fiskefrikadeller","recipeYield":"4","totalTime":"PT40M",
	   "recipeIngredient":["500 g torsk","1 løg"],
	   "recipeInstructions":[{"@type":"HowToStep","text":"Blend fisken."},{"@type":"HowToStep","text":"Steg dem."}]}]}</script></head>
	  <body><nav>Menu</nav><p>Lots of story</p></body></html>`
	got := fetch.PageText([]byte(ld))
	for _, want := range []string{"Title: Fiskefrikadeller", "Serves: 4", "- 500 g torsk", "1. Blend fisken.", "2. Steg dem."} {
		if !strings.Contains(got, want) {
			t.Errorf("json-ld text lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "story") {
		t.Error("page text used despite recipe data")
	}
	plain := fetch.PageText([]byte(`<html><body><header>Site</header><script>var x=1</script><h1>Soup</h1><ul><li>1 leek</li><li>2 potatoes</li></ul><footer>©</footer></body></html>`))
	if plain != "Soup\n1 leek\n2 potatoes" {
		t.Errorf("plain text: %q", plain)
	}
}

func TestLimits(t *testing.T) {
	t.Parallel()
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(strings.Repeat("a", fetch.MaxBytes+10)))
	}))
	defer big.Close()
	f := fetch.Fetcher{AllowPrivate: true}
	if _, err := f.Text(context.Background(), big.URL); err == nil || !strings.Contains(err.Error(), "2 MB") {
		t.Errorf("big page: %v", err)
	}
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("x"))
	}))
	defer img.Close()
	if _, err := f.Text(context.Background(), img.URL); err == nil {
		t.Error("image accepted as a page")
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<p>Pancakes</p><p>2 eggs</p>"))
	}))
	defer ok.Close()
	if got, err := f.Text(context.Background(), ok.URL); err != nil || got != "Pancakes\n2 eggs" {
		t.Errorf("page: %q %v", got, err)
	}
}
