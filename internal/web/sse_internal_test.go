package web

import (
	"net/http/httptest"
	"testing"
)

func TestWriteSSEMultiline(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := writeSSE(rec, "list", "<p>\r\na</p>\nb"); err != nil {
		t.Fatal(err)
	}
	want := "event: list\ndata: <p>\ndata: a</p>\ndata: b\n\n"
	if rec.Body.String() != want {
		t.Errorf("got %q want %q", rec.Body.String(), want)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"": "/", "/week": "/week", "//evil.example": "/", "/\\evil": "/", "https://evil.example": "/",
		"/login?next=/x": "/", "/invite/abc": "/invite/abc",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
