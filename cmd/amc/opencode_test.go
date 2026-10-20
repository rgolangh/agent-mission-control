package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoveredOpenCodeDoesNotReceiveConfiguredPassword(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	var authorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newOpenCodeClient(srv.URL, 2_000_000_000, "user-secret")
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("expected the password-protected discovered server to reject the unauthenticated probe")
	}
	if authorization != "" {
		t.Fatalf("discovered endpoint received Authorization: %q", authorization)
	}
}

func TestExplicitOpenCodeReceivesConfiguredPassword(t *testing.T) {
	var authorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newOpenCodeClient(srv.URL, 0, "user-secret")
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if authorization != "Basic b3BlbmNvZGU6dXNlci1zZWNyZXQ=" {
		t.Fatalf("explicit endpoint Authorization = %q", authorization)
	}
}
