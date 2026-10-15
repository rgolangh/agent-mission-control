package adapter

import "testing"

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"plain text, ünïcödé 🧑‍💻":        "plain text, ünïcödé 🧑‍💻", // ZWJ in the emoji survives
		"two\nlines\tand a tab":          "two\nlines\tand a tab",
		"\x1b]0;pwned\x07title":          "]0;pwnedtitle",          // OSC retitle
		"\x1b[2Jcleared":                 "[2Jcleared",             // clear screen
		"\x1b]52;c;ZWNobyBoaQ==\x07clip": "]52;c;ZWNobyBoaQ==clip", // OSC 52 clipboard
		"a\u009b31mb":                    "a31mb",                  // C1 CSI
		"run \u202etxt.exe":              "run txt.exe",            // right-to-left override
		"is\u2066olate\u2069d":           "isolated",               // isolates
		"zero\u200bwidth\ufeff":          "zerowidth",              // invisible
		"del\x7f":                        "del",
		"raw\x9bbyte":                    "raw\ufffdbyte", // invalid UTF-8 (8-bit CSI)
	} {
		if got := CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanEventCopiesTheAsk(t *testing.T) {
	orig := &Ask{ID: "q", Questions: []AskQuestion{{Question: "ok\x1b[2J?", Options: []string{"y\u202e", "n"}}}}
	ev := SourceEvent{Title: "t\x07", Message: "m\x1b", Location: "/l\u200b", Ask: orig}
	ev.Clean()
	if ev.Title != "t" || ev.Message != "m" || ev.Location != "/l" {
		t.Errorf("fields not cleaned: %+v", ev)
	}
	if q := ev.Ask.Questions[0]; q.Question != "ok[2J?" || q.Options[0] != "y" {
		t.Errorf("ask not cleaned: %+v", q)
	}
	if orig.Questions[0].Options[0] != "y\u202e" {
		t.Error("Clean modified the adapter's own Ask")
	}
}
