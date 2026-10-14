package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"mission-control/internal/adapter"
)

func TestRequireToken(t *testing.T) {
	s := NewStore()
	s.Apply(ev("ses_abcd1234", adapter.StatusRunning, "w", time.Now()))
	h := RequireToken("s3cret", Handler(s))
	for _, c := range []struct {
		name, path, auth string
		want             int
	}{
		{"no token", "/workflows", "", http.StatusUnauthorized},
		{"wrong token", "/workflows", "Bearer nope", http.StatusUnauthorized},
		{"basic auth is not the token", "/workflows", "Basic czNjcmV0", http.StatusUnauthorized},
		{"right token", "/workflows", "Bearer s3cret", http.StatusOK},
		{"healthz is open", "/healthz", "", http.StatusOK},
	} {
		req := httptest.NewRequest("GET", c.path, nil)
		req.Host = "127.0.0.1:4200"
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
		}
	}
	// The browser guards still apply behind the token.
	req := httptest.NewRequest("POST", "/workflows/abcd1234/send", nil)
	req.Host = "127.0.0.1:4200"
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain POST with token: %d", rec.Code)
	}
}

func TestCallOverUnixSocket(t *testing.T) {
	s := NewStore()
	s.Apply(ev("ses_abcd1234", adapter.StatusRunning, "working", time.Now()))
	sock := filepath.Join(t.TempDir(), "api.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: Handler(s)}
	go srv.Serve(l)
	defer srv.Close()

	wfs, err := FetchWorkflows(context.Background(), "unix:"+sock)
	if err != nil || len(wfs) != 1 || wfs[0].SourceID != "ses_abcd1234" {
		t.Fatalf("over socket: %v %+v", err, wfs)
	}
	if _, err := FetchWorkflow(context.Background(), "unix:"+sock, "../../etc"); err == nil {
		t.Error("a ref with slashes must not resolve")
	}
}
