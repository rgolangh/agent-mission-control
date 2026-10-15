import QtQuick

// Client for the mission-control daemon's HTTP API (see internal/daemon/http.go).
// Polls GET /workflows and exposes the list sorted by urgency. The daemon is
// the single source of truth: answering never changes local state, it only
// asks the daemon, and the next poll shows what the agent actually did.
QtObject {
  id: api

  property string baseUrl: "http://127.0.0.1:4200"
  // The daemon's TCP port requires its bearer token (regenerated on every
  // daemon start and removed when it stops; the widget re-reads the file).
  // No token, no request: while the daemon is down, whatever else holds its
  // port gets neither a token nor your answers.
  property string token: ""
  property int interval: 2000
  property bool active: true

  property var workflows: []
  property bool reachable: false
  property bool loaded: false // at least one poll finished, either way
  property string error: ""

  readonly property int waitingCount: count("needs_input")
  readonly property int stuckCount: count("stuck")
  readonly property int failedCount: count("failed")
  readonly property int runningCount: count("running")
  // Everything that won't move without you.
  readonly property int attentionCount: waitingCount + stuckCount + failedCount

  // A workflow just started needing you (see _noticeTransitions).
  signal needsYou(var workflow)

  property var _signatures: ({})
  property bool _primed: false
  property bool _inFlight: false

  // Urgency order: what needs you first, then what might (stuck, failed),
  // then what is busy, then what is finished. Newest first within a group.
  readonly property var _rank: ({ needs_input: 0, stuck: 1, failed: 2, running: 3, done: 4 })

  property Timer _timer: Timer {
    interval: api.interval
    running: api.active
    repeat: true
    triggeredOnStart: true
    onTriggered: api.refresh()
  }

  function count(status) {
    var n = 0
    for (var i = 0; i < workflows.length; i++)
      if (workflows[i].status === status && !workflows[i].stale) n++
    return n
  }

  function request(method, path, body, done) {
    if (!token) { done(0, null); return }
    var xhr = new XMLHttpRequest()
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== XMLHttpRequest.DONE) return
      var data = null
      try { data = JSON.parse(xhr.responseText) } catch (e) {}
      done(xhr.status, data)
    }
    xhr.open(method, baseUrl.replace(/\/$/, "") + path)
    xhr.setRequestHeader("Authorization", "Bearer " + token)
    if (body !== undefined) {
      // The daemon refuses POSTs that are not JSON (CSRF guard).
      xhr.setRequestHeader("Content-Type", "application/json")
      xhr.send(JSON.stringify(body))
    } else {
      xhr.send()
    }
  }

  function refresh() {
    if (_inFlight) return
    _inFlight = true
    request("GET", "/workflows", undefined, function(status, data) {
      _inFlight = false
      loaded = true
      if (status !== 200 || !data) {
        reachable = false
        error = status === 0 ? "mission-control is not running"
          : status === 401 ? "not authorized: cannot read the daemon's token file"
          : ("daemon returned " + status)
        return
      }
      reachable = true
      error = ""
      var list = (data.workflows || []).slice()
      list.sort(function(a, b) {
        var ra = api._rank[a.status], rb = api._rank[b.status]
        if (ra === undefined) ra = 5
        if (rb === undefined) rb = 5
        if (ra !== rb) return ra - rb
        return a.updated_at < b.updated_at ? 1 : (a.updated_at > b.updated_at ? -1 : 0)
      })
      _noticeTransitions(list)
      workflows = list
    })
  }

  // Emits needsYou when a workflow enters needs_input/stuck/failed, or a new
  // ask arrives while it waits. Silent on the first poll, so loading the
  // widget doesn't replay everything already waiting.
  function _noticeTransitions(list) {
    var seen = {}
    for (var i = 0; i < list.length; i++) {
      var w = list[i]
      var pending = (w.pending || []).map(function(a) { return a.id }).join(",")
      var sig = w.status + "|" + pending
      seen[w.id] = sig
      var attention = !w.stale && (w.status === "needs_input" || w.status === "stuck" || w.status === "failed")
      if (_primed && attention && _signatures[w.id] !== sig) needsYou(w)
    }
    _signatures = seen
    _primed = true
  }

  // ref: the workflow's source_id (exact match on the daemon, and unlike the
  // full id it has no ':' to escape in a path).
  // choice: an option label or free text; an array is several picks for one
  // multi-select question (sent as `answers`, so labels may contain commas).
  function answer(ref, askId, choice, done) {
    request("POST", "/workflows/" + encodeURIComponent(ref) + "/answer",
      Array.isArray(choice) ? { ask_id: askId, answers: [choice] } : { ask_id: askId, choice: choice },
      function(status, data) {
        if (done) done(status === 200, data && data.error ? data.error : (status === 0 ? "daemon unreachable" : ""))
        api.refresh()
      })
  }

  function send(ref, text, done) {
    request("POST", "/workflows/" + encodeURIComponent(ref) + "/send", { text: text },
      function(status, data) {
        if (done) done(status === 200, data && data.error ? data.error : (status === 0 ? "daemon unreachable" : ""))
        api.refresh()
      })
  }
}
