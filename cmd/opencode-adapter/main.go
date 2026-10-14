// Command opencode-adapter connects to a running `opencode serve` instance
// and prints normalized workflow events to stdout. This stands in for "push
// to the event bus" until the daemon exists — swap the onEvent callback in
// main() for an HTTP POST or a queue publish when that's ready.
//
// Usage:
//
//	opencode serve --port 4096 &
//	go run ./cmd/opencode-adapter -addr http://localhost:4096
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/opencode"
)

func main() {
	addr := flag.String("addr", "http://localhost:4096", "opencode server base URL")
	password := flag.String("password", os.Getenv("OPENCODE_SERVER_PASSWORD"), "basic auth password, if the server requires one")
	flag.Parse()

	client := opencode.New(*addr)
	client.Password = *password

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := client.Health(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: health check failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "continuing anyway — is `opencode serve` running at", *addr, "?")
	}

	onEvent := func(env opencode.Envelope) {
		normalized, ok := opencode.Normalize(env)
		if !ok {
			return
		}
		out, _ := json.Marshal(normalized)
		fmt.Println(string(out))
	}
	onLog := func(msg string) {
		fmt.Fprintln(os.Stderr, "[adapter]", msg)
	}

	client.SubscribeWithReconnect(ctx, 45*time.Second, onEvent, onLog)
}
