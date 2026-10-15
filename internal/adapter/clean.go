package adapter

import "strings"

// CleanText removes characters that let text pose as something else when it
// is displayed: C0 and C1 control characters (terminal escape sequences:
// retitling, clearing or rewriting the screen, OSC 52 clipboard writes,
// hyperlinks), bidirectional overrides and isolates (text that displays in a
// different order than it reads), and invisible zero-width characters.
// Invalid UTF-8 becomes U+FFFD (a raw 0x9b byte is CSI to an 8-bit terminal).
// Newlines and tabs are kept. Agent-written text is untrusted, and the
// daemon cleans it once, on the way in, for every consumer: CLI, desktop UIs,
// MCP and the log.
func CleanText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if strings.IndexFunc(s, unsafeRune) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return -1
		}
		return r
	}, s)
}

func unsafeRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20, r >= 0x7f && r <= 0x9f: // C0, DEL, C1
		return true
	case r == 0x061c, r == 0x200e, r == 0x200f, // bidi marks
		r >= 0x202a && r <= 0x202e, // bidi embeddings and overrides
		r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	case r == 0x200b, r == 0x2060, r == 0xfeff: // zero-width space, word joiner, BOM
		return true
	}
	return false
}

// Clean applies CleanText to every agent-written field of the event.
func (e *SourceEvent) Clean() {
	e.Message = CleanText(e.Message)
	e.Title = CleanText(e.Title)
	e.Location = CleanText(e.Location)
	if e.Ask != nil {
		a := *e.Ask // the adapter may still hold the original
		a.Questions = make([]AskQuestion, len(e.Ask.Questions))
		for i, q := range e.Ask.Questions {
			q.Question = CleanText(q.Question)
			q.Header = CleanText(q.Header)
			if q.Options != nil {
				opts := make([]string, len(q.Options))
				for j, o := range q.Options {
					opts[j] = CleanText(o)
				}
				q.Options = opts
			}
			a.Questions[i] = q
		}
		e.Ask = &a
	}
}
