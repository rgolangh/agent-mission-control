// Command fake-opencode is a scripted stand-in for `opencode serve`, used to
// record the README demo. It speaks just enough of OpenCode's HTTP/SSE API
// (the endpoints internal/opencode uses) to play a fixed story: four agent
// sessions that end up running, stuck, asking a question and asking for a
// permission, and that react to answers and follow-ups the way a real agent
// would. No model, no tokens, the same result every take.
//
//	go run ./docs/demo/fake-opencode -listen 127.0.0.1:4396
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type todo struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

type session struct {
	ID, Title, Dir string
	Busy           bool
	Prompt         string // the user's last message
	Reply          string // the assistant's final words, once idle
	Todos          []todo
}

type server struct {
	mu        sync.Mutex
	sessions  map[string]*session
	order     []string
	questions map[string]map[string]any // request ID -> question.asked properties
	perms     map[string]map[string]any // request ID -> permission.asked properties
	subs      map[chan []byte]bool
	started   bool
	beat      time.Duration
}

func main() {
	listen := flag.String("listen", "127.0.0.1:4396", "address to serve on")
	pace := flag.Duration("pace", 700*time.Millisecond, "delay between scripted events")
	flag.Parse()

	s := &server{
		sessions:  map[string]*session{},
		questions: map[string]map[string]any{},
		perms:     map[string]map[string]any{},
		subs:      map[chan []byte]bool{},
		beat:      10 * time.Second,
	}
	s.add(&session{ID: "ses_7a1fRw9kAuthMw", Title: "Refactor auth middleware", Dir: "~/src/api",
		Prompt: "Move session handling into the auth middleware."})
	s.add(&session{ID: "ses_3c88Lq2pSyncTst", Title: "Fix flaky sync test", Dir: "~/src/sync",
		Prompt: "TestSyncRetry fails about one run in five. Find out why and fix it."})
	s.add(&session{ID: "ses_9d02Hx4mDepsBmp", Title: "Bump Go dependencies", Dir: "~/src/api",
		Prompt: "Update all modules to their latest minor versions."})
	s.add(&session{ID: "ses_5e6bNv8rRelNote", Title: "Write release notes for v0.2", Dir: "~/src/docs",
		Prompt: "Draft the v0.2 release notes from the merged PRs."})

	http.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"healthy": true, "version": "demo"})
	})
	http.HandleFunc("GET /event", s.events)
	http.HandleFunc("GET /session/status", s.status)
	http.HandleFunc("GET /session/{id}", s.info)
	http.HandleFunc("GET /session/{id}/message", s.messages)
	http.HandleFunc("GET /session/{id}/todo", s.todos)
	http.HandleFunc("GET /question", func(w http.ResponseWriter, r *http.Request) { s.pending(w, s.questions) })
	http.HandleFunc("GET /permission", func(w http.ResponseWriter, r *http.Request) { s.pending(w, s.perms) })
	http.HandleFunc("POST /question/{id}/reply", s.replyQuestion)
	http.HandleFunc("POST /permission/{id}/reply", s.replyPermission)
	http.HandleFunc("POST /session/{id}/prompt_async", s.prompt)

	go s.script(*pace)
	log.Printf("fake opencode on http://%s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

func (s *server) add(ss *session) {
	ss.Busy = true
	s.sessions[ss.ID] = ss
	s.order = append(s.order, ss.ID)
}

// script plays the opening once the daemon has subscribed: everything is busy
// at first, then the sessions settle into the states the demo shows.
func (s *server) script(pace time.Duration) {
	for {
		s.mu.Lock()
		ok := s.started
		s.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2 * pace)

	s.mu.Lock()
	s.sessions["ses_5e6bNv8rRelNote"].Todos = []todo{
		{"Collect merged PRs since v0.1.0", "high", "completed"},
		{"Group changes by area", "medium", "completed"},
		{"Write highlights", "medium", "in_progress"},
		{"Add upgrade notes", "low", "pending"},
	}
	s.emit("todo.updated", map[string]any{"sessionID": "ses_5e6bNv8rRelNote", "todos": s.sessions["ses_5e6bNv8rRelNote"].Todos})
	s.mu.Unlock()
	time.Sleep(pace)

	s.mu.Lock()
	s.idle("ses_3c88Lq2pSyncTst", "The retry test races the fake clock. "+
		"I couldn't run the integration tests: Docker isn't running here.")
	s.mu.Unlock()
	time.Sleep(pace)

	s.mu.Lock()
	s.ask("ses_9d02Hx4mDepsBmp", "per_4kq81", true, map[string]any{
		"id": "per_4kq81", "sessionID": "ses_9d02Hx4mDepsBmp", "permission": "bash",
		"patterns": []string{"go get -u ./..."}, "always": []string{"go get *"}, "metadata": map[string]any{},
	})
	s.mu.Unlock()
	time.Sleep(pace)

	s.mu.Lock()
	s.ask("ses_7a1fRw9kAuthMw", "que_8pz20", false, map[string]any{
		"id": "que_8pz20", "sessionID": "ses_7a1fRw9kAuthMw",
		"questions": []map[string]any{{
			"header":   "Session store",
			"question": "Which session store should the middleware use?",
			"options": []map[string]any{
				{"label": "Redis", "description": "shared across replicas"},
				{"label": "Postgres", "description": "reuse the main database"},
				{"label": "In-memory", "description": "single instance only"},
			},
		}},
	})
	s.mu.Unlock()
}

// emit sends one /event frame to every subscriber. Callers hold s.mu.
func (s *server) emit(typ string, props any) {
	b, _ := json.Marshal(map[string]any{"type": typ, "properties": props})
	for ch := range s.subs {
		select {
		case ch <- b:
		default:
		}
	}
}

func (s *server) ask(sid, id string, permission bool, props map[string]any) {
	if permission {
		s.perms[id] = props
		s.emit("permission.asked", props)
	} else {
		s.questions[id] = props
		s.emit("question.asked", props)
	}
}

func (s *server) idle(sid, reply string) {
	ss := s.sessions[sid]
	ss.Busy, ss.Reply = false, reply
	s.emit("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "idle"}})
	s.emit("session.idle", map[string]any{"sessionID": sid})
}

func (s *server) busy(sid string) {
	s.sessions[sid].Busy = true
	s.emit("session.status", map[string]any{"sessionID": sid, "status": map[string]any{"type": "busy"}})
}

// later runs f under the lock after d, for the agent "working" on an answer.
func (s *server) later(d time.Duration, f func()) {
	time.AfterFunc(d, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		f()
	})
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan []byte, 64)
	s.mu.Lock()
	s.subs[ch] = true
	s.started = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	fmt.Fprintf(w, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
	fl.Flush()
	tick := time.NewTicker(s.beat)
	defer tick.Stop()
	for {
		select {
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
		case <-tick.C:
			fmt.Fprintf(w, "data: {\"type\":\"server.heartbeat\",\"properties\":{}}\n\n")
		case <-r.Context().Done():
			return
		}
		fl.Flush()
	}
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	for id, ss := range s.sessions {
		if ss.Busy {
			out[id] = map[string]any{"type": "busy"}
		}
	}
	writeJSON(w, out)
}

func (s *server) session(w http.ResponseWriter, r *http.Request) *session {
	ss := s.sessions[r.PathValue("id")]
	if ss == nil {
		http.Error(w, "no such session", http.StatusNotFound)
	}
	return ss
}

func (s *server) info(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.session(w, r); ss != nil {
		writeJSON(w, map[string]any{"id": ss.ID, "title": ss.Title, "directory": ss.Dir})
	}
}

func (s *server) messages(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.session(w, r)
	if ss == nil {
		return
	}
	msgs := []any{map[string]any{
		"info":  map[string]any{"role": "user"},
		"parts": []any{map[string]any{"type": "text", "text": ss.Prompt}},
	}}
	if ss.Reply != "" {
		msgs = append(msgs, map[string]any{
			"info":  map[string]any{"role": "assistant", "finish": "stop"},
			"parts": []any{map[string]any{"type": "text", "text": ss.Reply}},
		})
	}
	writeJSON(w, msgs)
}

func (s *server) todos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.session(w, r); ss != nil {
		writeJSON(w, append([]todo{}, ss.Todos...))
	}
}

func (s *server) pending(w http.ResponseWriter, m map[string]map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, v := range m {
		out = append(out, v)
	}
	writeJSON(w, out)
}

func (s *server) replyQuestion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Answers [][]string `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Answers) == 0 || len(body.Answers[0]) == 0 {
		http.Error(w, "answers required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	q := s.questions[id]
	if q == nil {
		http.Error(w, "no such question", http.StatusNotFound)
		return
	}
	delete(s.questions, id)
	sid := q["sessionID"].(string)
	choice := body.Answers[0][0]
	s.emit("question.replied", map[string]any{"sessionID": sid, "requestID": id, "answers": body.Answers})
	s.later(4*time.Second, func() {
		s.idle(sid, fmt.Sprintf("Sessions now live in the auth middleware, on %s. Tests pass.", choice))
	})
	writeJSON(w, true)
}

func (s *server) replyPermission(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reply string `json:"reply"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	p := s.perms[id]
	if p == nil {
		http.Error(w, "no such permission request", http.StatusNotFound)
		return
	}
	delete(s.perms, id)
	sid := p["sessionID"].(string)
	s.emit("permission.replied", map[string]any{"sessionID": sid, "requestID": id, "reply": body.Reply})
	reply := "Bumped 6 modules; go vet and go test ./... pass."
	if body.Reply == "reject" {
		reply = "Understood, I won't run go get. The modules that need updating are listed in deps.md."
	}
	s.later(3*time.Second, func() { s.idle(sid, reply) })
	writeJSON(w, true)
}

func (s *server) prompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Parts) == 0 {
		http.Error(w, "parts required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.session(w, r)
	if ss == nil {
		return
	}
	ss.Prompt, ss.Reply = body.Parts[0].Text, ""
	s.busy(ss.ID)
	reply := "Done. Pushed the change and opened a pull request."
	if strings.Contains(strings.ToLower(ss.Prompt), "docker") || ss.ID == "ses_3c88Lq2pSyncTst" {
		reply = "Fixed the timer race; TestSyncRetry passed 200 runs in a row."
	}
	s.later(4*time.Second, func() { s.idle(ss.ID, reply) })
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
