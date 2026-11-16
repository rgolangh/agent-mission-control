package terminal

import "testing"

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestDetectKitty(t *testing.T) {
	got := Detect(env(map[string]string{"KITTY_WINDOW_ID": "7", "KITTY_LISTEN_ON": "unix:/run/user/1000/kitty-3858"}))
	if got == nil || *got != (Terminal{Kind: "kitty", Address: "unix:/run/user/1000/kitty-3858", Target: "7"}) {
		t.Fatalf("Detect = %+v", got)
	}
	if got := Detect(env(map[string]string{"KITTY_WINDOW_ID": "7"})); got != nil {
		t.Errorf("no remote control socket: Detect = %+v, want nil", got)
	}
	if got := Detect(env(nil)); got != nil {
		t.Errorf("not in a terminal: Detect = %+v, want nil", got)
	}
}

func TestValid(t *testing.T) {
	for _, tc := range []struct {
		t    Terminal
		want bool
	}{
		{Terminal{"kitty", "unix:/run/user/1000/kitty-1", "7"}, true},
		{Terminal{"kitty", "unix:@mykitty", "12"}, true},
		{Terminal{"kitty", "tcp:localhost:1234", "7"}, false}, // sockets only: a port is anyone's
		{Terminal{"kitty", "unix:relative/path", "7"}, false},
		{Terminal{"kitty", "unix:/tmp/k\n", "7"}, false},
		{Terminal{"kitty", "--help", "7"}, false},
		{Terminal{"kitty", "unix:/tmp/k", "0"}, false},
		{Terminal{"kitty", "unix:/tmp/k", "07"}, false},
		{Terminal{"kitty", "unix:/tmp/k", "7 or 8"}, false},
		{Terminal{"kitty", "unix:/tmp/k", "-1"}, false},
		{Terminal{"xterm", "unix:/tmp/k", "7"}, false},
	} {
		if got := tc.t.Valid(); got != tc.want {
			t.Errorf("%+v.Valid() = %v, want %v", tc.t, got, tc.want)
		}
	}
	var none *Terminal
	if none.Valid() {
		t.Error("nil is valid")
	}
}
