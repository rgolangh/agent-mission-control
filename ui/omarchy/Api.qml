import QtQuick

// Client for the mission-control daemon's HTTP API (see internal/daemon/http.go).
// Polls GET /workflows and exposes the list sorted by urgency. The daemon is
// the single source of truth: answering never changes local state, it only
// asks the daemon, and the next poll shows what the agent actually did.
QtObject {
  id: api

  property string baseUrl: "http://127.0.0.1:4200"
  property int interval: 2000
  property bool active: true

  property var workflows: []
  property bool reachable: false
  property bool loaded: false // at least one poll finished, either way
  property string error: ""

  readonly property int waitingCount: count("needs_input")
  readonly property int runningCount: count("running")

  // A workflow just started waiting on an ask we have not seen before.
  // Not emitted for asks already pending when the widget first loads.
  signal newAsk(var workflow, var ask)

  property var _knownAsks: ({})
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
    var xhr = new XMLHttpRequest()
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== XMLHttpRequest.DONE) return
      var data = null
      try { data = JSON.parse(xhr.responseText) } catch (e) {}
      done(xhr.status, data)
    }
    xhr.open(method, baseUrl.replace(/\/$/, "") + path)
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
        error = status === 0 ? "mission-control is not running" : ("daemon returned " + status)
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
      _noticeAsks(list)
      workflows = list
    })
  }

  function _noticeAsks(list) {
    var seen = {}
    for (var i = 0; i < list.length; i++) {
      var pending = list[i].pending || []
      for (var j = 0; j < pending.length; j++) {
        var key = list[i].id + "/" + pending[j].id
        seen[key] = true
        if (_primed && !_knownAsks[key]) newAsk(list[i], pending[j])
      }
    }
    _knownAsks = seen
    _primed = true
  }

  // ref: the workflow's source_id (exact match on the daemon, and unlike the
  // full id it has no ':' to escape in a path).
  function answer(ref, askId, choice, done) {
    request("POST", "/workflows/" + encodeURIComponent(ref) + "/answer",
      { ask_id: askId, choice: choice },
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
