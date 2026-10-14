package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

func do2(t *testing.T, h http.Handler, method, target, host, ctype, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if host != "" {
		req.Host = host
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHTTPAnswerRoundTripAndStatusCodes(t *testing.T) {
	s, r := bound(t, question("q1", false, false, "Tabs", "Spaces"))
	h := Handler(s)
	const js = "application/json"

	rec := do2(t, h, "POST", "/workflows/abcd1234/answer", "localhost:4200", js, `{"choice":"2"}`)
	if rec.Code != 200 || len(r.answers) != 1 || r.answers[0][0][0] != "Spaces" {
		t.Fatalf("answer: %d %s / %v", rec.Code, rec.Body, r.answers)
	}

	for _, tc := range []struct {
		name, target, body string
		want               int
	}{
		{"unknown workflow", "/workflows/zzzz9999/answer", `{"choice":"1"}`, http.StatusNotFound},
		{"invalid choice", "/workflows/abcd1234/answer", `{"choice":"bogus"}`, http.StatusBadRequest},
		{"unknown field", "/workflows/abcd1234/answer", `{"chioce":"1"}`, http.StatusBadRequest},
		{"malformed json", "/workflows/abcd1234/answer", `{`, http.StatusBadRequest},
	} {
		if rec := do2(t, h, "POST", tc.target, "localhost", js, tc.body); rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body)
		}
	}

	// GET by suffix works and exposes the pending ask with its options.
	rec = do2(t, h, "GET", "/workflows/abcd1234", "127.0.0.1:4200", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Spaces"`) {
		t.Errorf("GET by suffix: %d %s", rec.Code, rec.Body)
	}
}

func TestHTTPGuards(t *testing.T) {
	s := NewStore()
	e := ev("ses_abcd1234", adapter.StatusRunning, "w", time.Now())
	s.Apply(e)
	h := Handler(s)

	// DNS rebinding: a non-loopback Host is refused for reads and writes.
	for _, host := range []string{"evil.example.com", "evil.example.com:4200", "10.0.0.5:4200"} {
		if rec := do2(t, h, "GET", "/workflows", host, "", ""); rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: got %d, want 403", host, rec.Code)
		}
	}
	for _, host := range []string{"localhost:4200", "127.0.0.1:4200", "[::1]:4200", "localhost"} {
		if rec := do2(t, h, "GET", "/workflows", host, "", ""); rec.Code != 200 {
			t.Errorf("Host %q: got %d, want 200", host, rec.Code)
		}
	}

	// Cross-site "simple" POSTs skip CORS preflight; only JSON forces one.
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data"} {
		if rec := do2(t, h, "POST", "/workflows/abcd1234/send", "localhost", ct, `{"text":"hi"}`); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: got %d, want 415", ct, rec.Code)
		}
	}
	if rec := do2(t, h, "POST", "/workflows/abcd1234/send", "localhost", "application/json; charset=utf-8", `{"text":"hi"}`); rec.Code == http.StatusUnsupportedMediaType {
		t.Errorf("JSON with charset was rejected")
	}
}
