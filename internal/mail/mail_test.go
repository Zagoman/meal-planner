package mail_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/mail"
)

func TestResendSendsPlainMessage(t *testing.T) {
	t.Parallel()
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()
	r := mail.NewResend("re_test", "Meal Planner <meals@example.test>")
	r.URL = srv.URL
	m := mail.Simple("a@example.test", "Your sign-in link", "Tap to sign in.", "Sign in", "https://meals.example.test/auth/tok<&>", "Expires in 15 minutes.")
	if err := r.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer re_test" || got["subject"] != "Your sign-in link" || got["from"] != "Meal Planner <meals@example.test>" {
		t.Errorf("request: %q %v", auth, got)
	}
	h, _ := got["html"].(string)
	if !strings.Contains(h, "tok&lt;&amp;&gt;") || strings.Contains(h, "<img") {
		t.Errorf("html not escaped or has an image: %s", h)
	}
	if txt, _ := got["text"].(string); !strings.Contains(txt, "Sign in: https://meals.example.test/auth/tok<&>") {
		t.Errorf("text: %s", txt)
	}
}

func TestResendError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"domain not verified"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	r := mail.NewResend("re_test", "x@example.test")
	r.URL = srv.URL
	if err := r.Send(context.Background(), mail.Message{To: "a@example.test"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err %v", err)
	}
}
