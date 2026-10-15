package daemon

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rgolangh/agent-mission-control/internal/adapter"
)

// Verdict is the daemon's call on a finished turn.
type Verdict struct {
	Status  adapter.Status
	Message string // what to show, carrying the evidence ("asks: …", "blocked: …")
	Rule    string // which rule decided, for debugging and tests
}

// JudgeTurn reads how a source's turn ended and replaces the turn-end
// event's fallback status with Judge's verdict. If the transcript can't be
// read, the fallback stands and the message says why.
func JudgeTurn(ctx context.Context, r adapter.TurnReader, ev *adapter.SourceEvent) (Verdict, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	turn, err := r.LastTurn(ctx, ev.SourceID)
	if err != nil {
		ev.Message = "went idle (could not read the transcript to tell done from stuck)"
		return Verdict{}, err
	}
	v := Judge(turn)
	ev.Status, ev.Message, ev.Rule = v.Status, v.Message, v.Rule
	return v, nil
}

// Judge decides what an agent that just went idle is waiting for, from the
// evidence its adapter collected. No LLM: an ordered list of rules, first
// match wins. Earlier rules use hard facts (errors, finish reasons, missing
// output); later ones read the final message. Every verdict quotes what it
// matched, so a wrong call is easy to spot.
//
//	 1 aborted            stuck        the user interrupted it
//	 2 context-overflow   stuck        ran out of context
//	 3 output-limit       stuck        cut off mid-answer (error or finish=length)
//	 4 content-filter     stuck        the provider refused
//	 5 auth-error         failed       provider credentials
//	 6 error              failed       any other provider/runtime error
//	 7 no-reply           stuck        idle with nothing after the user's message
//	 8 tool-failed        stuck        a failing tool call was the last thing it did
//	 9 no-final-message   stuck        it stopped without saying anything
//	10 mid-step           stuck        stopped while still calling tools
//	11 greeting           done         "How can I help?": ready for a task
//	12 question           needs_input  ends by asking you something (not an offer),
//	                                   incl. questions in a closing list
//	   buried-question    needs_input  asks you something, then adds context
//	                                   ("Which DB? Postgres is the default."),
//	                                   in the last paragraph or the opening
//	13 request            needs_input  "please confirm…", "once you confirm…",
//	                                   "tell me and I'll…", "To build this I need:"
//	14 blocker            stuck        reports it could not do something (closing
//	                                   sentences or headline)
//	15 open-todos         stuck        its own todo list is not finished
//	16 offer              done         finished, offers optional extra work
//	17 finished           done         none of the above
func Judge(t adapter.TurnEnd) Verdict {
	switch t.Error {
	case adapter.TurnErrorAborted:
		return Verdict{adapter.StatusStuck, "interrupted before it finished", "aborted"}
	case adapter.TurnErrorContextOverflow:
		return Verdict{adapter.StatusStuck, "ran out of context: compact or start a new session", "context-overflow"}
	case adapter.TurnErrorOutputLength:
		return Verdict{adapter.StatusStuck, "hit the output limit mid-answer", "output-limit"}
	case adapter.TurnErrorContentFilter:
		return Verdict{adapter.StatusStuck, "stopped by the provider's content filter", "content-filter"}
	case adapter.TurnErrorAuth:
		return Verdict{adapter.StatusFailed, withDetail("provider authentication failed", t.ErrorMessage), "auth-error"}
	case "":
	default:
		return Verdict{adapter.StatusFailed, withDetail("error", t.ErrorMessage), "error"}
	}
	switch t.Finish {
	case "length":
		return Verdict{adapter.StatusStuck, "hit the output limit mid-answer", "output-limit"}
	case "content-filter":
		return Verdict{adapter.StatusStuck, "stopped by the provider's content filter", "content-filter"}
	}
	if !t.Replied {
		return Verdict{adapter.StatusStuck, "went idle without replying", "no-reply"}
	}

	text := cleanText(t.Text)
	if t.LastToolFailed != "" {
		return Verdict{adapter.StatusStuck, "stopped after a failed " + t.LastToolFailed + " call", "tool-failed"}
	}
	if text == "" {
		return Verdict{adapter.StatusStuck, "stopped without a final message", "no-final-message"}
	}
	if t.Finish == "tool-calls" {
		return Verdict{adapter.StatusStuck, "stopped mid-step: " + summary(text), "mid-step"}
	}

	q, isQuestion := closingQuestion(text)
	buried := false
	if !isQuestion {
		q, isQuestion = buriedQuestion(text)
		buried = isQuestion
	}
	if isQuestion && greetingRe.MatchString(q) {
		return Verdict{adapter.StatusDone, "ready for a task", "greeting"}
	}
	offer := isQuestion && isOffer(q)
	if isQuestion && !offer {
		if buried {
			return Verdict{adapter.StatusNeedsInput, "asks: " + q, "buried-question"}
		}
		return Verdict{adapter.StatusNeedsInput, "asks: " + q, "question"}
	}
	if r := closingRequest(text); r != "" {
		return Verdict{adapter.StatusNeedsInput, "asks: " + r, "request"}
	}
	if b := blocker(text); b != "" {
		return Verdict{adapter.StatusStuck, "blocked: " + b, "blocker"}
	}
	if t.TodosOpen > 0 {
		return Verdict{adapter.StatusStuck, plural(t.TodosOpen, "todo") + " of " + strconv.Itoa(t.TodosTotal) + " still open: " + summary(text), "open-todos"}
	}
	if offer {
		sum := summary(text)
		if strings.Contains(sum, strings.TrimSuffix(q, "?")) || strings.Contains(q, sum) {
			return Verdict{adapter.StatusDone, "finished; offers: " + q, "offer"}
		}
		return Verdict{adapter.StatusDone, "finished: " + sum + " (offers: " + q + ")", "offer"}
	}
	return Verdict{adapter.StatusDone, "finished: " + summary(text), "finished"}
}

// ---------------------------------------------------------------- text

var (
	thinkRe     = regexp.MustCompile(`(?s)<think>.*?</think>|^.*?</think>`)
	fenceRe     = regexp.MustCompile("(?s)```.*?```")
	listItemRe  = regexp.MustCompile(`^\s*([-*•]|\d+[.)]|[a-zA-Z][.)])\s+`)
	sentenceEnd = regexp.MustCompile(`[.!?…](\s+|$)`)
	markdownRe  = regexp.MustCompile("[*_`#>]+")
	greetingRe  = regexp.MustCompile(`(?i)^(hi|hello|hey)?[!,. ]*(what|how) can i (help|do for you|assist)|^(is there )?anything else\b`)
	offerRe     = regexp.MustCompile(`(?i)^(so,? |and |also,? )?((do you )?want me to|would you like( me)? to|should i also|shall i also|i can also|if you('d| would) like|let me know if|need anything else|anything else)\b|\b(want|like) me to (also|additionally)\b`)
	mainWorkRe  = regexp.MustCompile(`(?i)\b(proceed|go ahead|continue|start|begin|move forward)\b`)
	requestRe   = regexp.MustCompile(`(?i)^(please |could you |can you )?(let me know|tell me|confirm|choose|pick|select|decide|provide|share|clarify)\b.*\b(which|what|whether|how|where|when|who|your|if you (prefer|want)|or)\b`)
	questionyRe = regexp.MustCompile(`(?i)\b(which|prefer|choose|pick|select|options?|questions?|decide|decisions?|need|confirm|your call|let me know|tell me)\b`)
	waitingRe   = regexp.MustCompile(`(?i)\b(once|when|after|as soon as) you(?:'ve| have)? (confirm|decide|choose|reply|answer|approve|pick|grant|rename|fix|let me know|tell me|say)\b|\btell me (and|when|once|which|what|whether|if)\b|\blet me know (when|once|which|what|whether|how)\b|\byour call\b|\bwaiting (for|on) (you|your)\b|\bI'll wait\b`)
	// A question addressed to the user: what they should decide, prefer or
	// allow. Agents also pose questions they answer themselves ("What was
	// wrong? A stale cache."), which have none of these.
	directedRe = regexp.MustCompile(`(?i)\b(should|shall|can|may|could|do|would) (i|we)\b|\b(do|would|did|could|can|will|are|have|should) you\b|\byou(?:'d)? (want|prefer|like|need|rather)\b|\bwant me\b|\byour (call|preference|choice|decision|thoughts|input|approval|take)\b|\bwhich (one|option|approach|of|do|would|should)\b|\b(ok|okay|alright|fine) (to|with|if)\b|\bis (that|this|it) (ok|okay|fine|alright|acceptable)\b|\bprefer\b`)
	whyRe      = regexp.MustCompile(`(?i)^(why|how come)\b`)
	answeredRe = regexp.MustCompile(`(?i)^(yes|no|yep|nope|because|it does|it doesn'?t)\b`)
	paraRe     = regexp.MustCompile(`\n\s*\n`)
	resolvedRe = regexp.MustCompile(`(?i)\b(fixed|resolved|now (works|passes|pass)|all (tests )?pass(es|ed)?|no longer|was able to|succeeded|works now|(and|but) it works|works fine|confirmed|verified)\b`)
	blockerRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bI(?: was|'m| am)? (?:couldn'?t|could not|can'?t|cannot|wasn'?t able to|was not able to|unable to|not able to)\b`),
		regexp.MustCompile(`(?i)\b(?:could not|couldn'?t|unable to|not able to|failed to|fails? to) (?:complete|finish|proceed|continue|resolve|fix|find|locate|access|reach|connect|install|build|compile|run|start|load|authenticate|reproduce)\b`),
		regexp.MustCompile(`(?i)\bI (?:don'?t|do not) have (?:access|permission|the (?:credentials|permissions?|tools?))\b`),
		regexp.MustCompile(`(?i)\b(?:no|without) (?:access to|permission to|credentials for)\b`),
		regexp.MustCompile(`(?i)\b(?:is|are|remains?|still) (?:blocked|failing|broken)\b`),
		regexp.MustCompile(`(?i)\b(?:tests?|build|lint(?:er)?|type ?check|compilation|ci)\s+(?:still\s+)?(?:fails?|failed|failing|errors?|is failing|are failing)\b`),
		regexp.MustCompile(`(?i)\bI'?m (?:not sure|unsure)\b|\bI am (?:not sure|unsure)\b`),
		regexp.MustCompile(`(?i)\b(?:needs?|requires?) (?:your|manual|human) (?:input|action|intervention|help|review|decision|approval)\b`),
		regexp.MustCompile(`(?i)\b(?:stuck on|blocked (?:on|by)|gave up|giving up|no way to)\b`),
		regexp.MustCompile(`(?i)\b(?:permission denied|access denied|rate[- ]limit(?:ed)?|quota exceeded|timed out|not found in PATH|command not found)\b`),
	}
)

// cleanText drops reasoning tags and code blocks: questions and blockers live
// in the prose, and a code sample ending in "?" must not count.
func cleanText(s string) string {
	s = thinkRe.ReplaceAllString(s, "")
	s = fenceRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// sentences splits prose into trimmed sentences, markdown removed.
func sentences(s string) []string {
	var out []string
	for _, l := range lines(s) {
		l = listItemRe.ReplaceAllString(l, "")
		l = strings.TrimSpace(markdownRe.ReplaceAllString(l, ""))
		start := 0
		for _, m := range sentenceEnd.FindAllStringIndex(l, -1) {
			if sent := strings.TrimSpace(l[start:m[1]]); sent != "" {
				out = append(out, sent)
			}
			start = m[1]
		}
		if rest := strings.TrimSpace(l[start:]); rest != "" {
			out = append(out, rest)
		}
	}
	return out
}

// closingQuestion finds a question the message ends on: the last sentence
// before any closing list ("Which do you prefer?" followed by "1. …"), or a
// question inside that closing list ("1. **Which desktop?** GNOME or KDE.",
// as agents write a batch of decisions).
func closingQuestion(text string) (string, bool) {
	ls := lines(text)
	i := len(ls) - 1
	for i >= 0 && listItemRe.MatchString(ls[i]) {
		i--
	}
	if i < 0 {
		return "", false
	}
	ss := sentences(ls[i])
	if len(ss) == 0 {
		return "", false
	}
	if last := ss[len(ss)-1]; strings.HasSuffix(last, "?") {
		return truncate(last, 160), true
	}
	for _, item := range ls[i+1:] {
		for _, s := range sentences(item) {
			if strings.HasSuffix(s, "?") {
				return truncate(s, 160), true
			}
		}
	}
	return "", false
}

// buriedQuestion finds a question addressed to the user that is followed by
// more text: in the last paragraph ("Which database should I use? Postgres
// is the default.") or among the first two sentences of the message
// ("Should I delete these? They were superseded." and then details). A
// question in the middle of a long report is not counted: there it is
// usually narration. Questions that are not addressed to the user, start
// with "why", or are answered by the next sentence ("Does it work? Yes.")
// are skipped.
func buriedQuestion(text string) (string, bool) {
	var paras [][]string
	for _, p := range paraRe.Split(text, -1) {
		if ss := sentences(p); len(ss) > 0 {
			paras = append(paras, ss)
		}
	}
	if len(paras) == 0 {
		return "", false
	}
	check := func(ss []string, n int) (string, bool) {
		for i, s := range ss {
			if i == n {
				break
			}
			if !strings.HasSuffix(s, "?") || !directedRe.MatchString(s) || whyRe.MatchString(s) {
				continue
			}
			if i+1 < len(ss) && answeredRe.MatchString(ss[i+1]) {
				continue
			}
			return truncate(s, 160), true
		}
		return "", false
	}
	last := paras[len(paras)-1]
	if q, ok := check(last, len(last)); ok {
		return q, true
	}
	return check(paras[0], 2)
}

// closingListIntro returns the line introducing a closing list when it says
// the list is what the agent needs from you ("Pick one:", "Two things need
// your call:", "To build this I need:").
func closingListIntro(text string) string {
	ls := lines(text)
	i := len(ls) - 1
	for i >= 0 && listItemRe.MatchString(ls[i]) {
		i--
	}
	if i < 0 || i == len(ls)-1 {
		return ""
	}
	ss := sentences(ls[i])
	if len(ss) == 0 {
		return ""
	}
	if intro := ss[len(ss)-1]; strings.HasSuffix(intro, ":") && questionyRe.MatchString(intro) {
		return truncate(intro, 160)
	}
	return ""
}

// isOffer: the work is done and the question proposes optional extra work
// ("Want me to also add tests?"), as opposed to needing a decision before it
// can go on ("Should I proceed with the migration?").
func isOffer(q string) bool {
	return offerRe.MatchString(q) && !mainWorkRe.MatchString(q)
}

// closingRequest finds a request for input in the last three sentences:
// imperative ("Please confirm which database to use.") or saying it waits
// on you ("I'll run it once you confirm the name.", "Tell me and I'll push."),
// or a closing list introduced as what it needs from you.
func closingRequest(text string) string {
	ss := sentences(text)
	for i := len(ss) - 1; i >= 0 && i >= len(ss)-3; i-- {
		if (requestRe.MatchString(ss[i]) || waitingRe.MatchString(ss[i])) && !offerRe.MatchString(ss[i]) {
			return truncate(ss[i], 160)
		}
	}
	return closingListIntro(text)
}

// blocker finds a sentence saying it could not do something, skipping ones
// that say the problem was then solved. It looks at the closing sentences,
// then the headline: agents often lead with the outcome ("Ready to push,
// but I couldn't push it: …").
func blocker(text string) string {
	ss := sentences(text)
	var idx []int
	for i := len(ss) - 1; i >= 0 && i >= len(ss)-6; i-- {
		idx = append(idx, i)
	}
	for i := 0; i < 2 && i < len(ss)-6; i++ {
		idx = append(idx, i)
	}
	for _, i := range idx {
		if resolvedRe.MatchString(ss[i]) {
			continue
		}
		for _, re := range blockerRes {
			if re.MatchString(ss[i]) {
				return truncate(ss[i], 160)
			}
		}
	}
	return ""
}

// summary is the message's first line of prose (headings skipped unless
// there is nothing else), as a short label.
func summary(text string) string {
	ls := lines(text)
	if len(ls) == 0 {
		return ""
	}
	first := ls[0]
	for _, l := range ls {
		if !strings.HasPrefix(l, "#") {
			first = l
			break
		}
	}
	s := strings.TrimSpace(markdownRe.ReplaceAllString(listItemRe.ReplaceAllString(first, ""), ""))
	return truncate(s, 140)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func withDetail(what, detail string) string {
	if detail = strings.TrimSpace(detail); detail == "" {
		return what
	}
	return what + ": " + truncate(detail, 160)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
