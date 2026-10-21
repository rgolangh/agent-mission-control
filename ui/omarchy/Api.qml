import QtQuick
import Quickshell.Io

// Client for the agent-mission-control daemon's HTTP API (see internal/daemon/http.go).
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

  // Explaining a daemon that doesn't answer (daemonState): the last poll's
  // HTTP status, plus what is on disk, probed while it is down.
  property string runtimeDir: ""
  readonly property string tokenPath: runtimeDir + "/token"
  readonly property string service: "agent-mission-control.service"
  readonly property string releases: "https://github.com/rgolangh/agent-mission-control/releases"
  property int lastStatus: 0
  property bool lastHadToken: false
  property bool probed: false
  property bool installed: false
  property bool unit: false
  property bool socket: false
  property double _probedAt: 0
  readonly property var down: daemonState({
    status: lastStatus, token: lastHadToken, probed: probed, installed: installed,
    unit: unit, socket: socket, url: baseUrl, tokenPath: tokenPath })

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

  // A static script with the runtime dir as $1: nothing from agents or
  // settings is ever shell text. Same paths as probe() in ui/gnome/client.js.
  property Process _probe: Process {
    command: ["sh", "-c",
      "{ command -v amc || [ -x \"$HOME/.local/bin/amc\" ] || [ -x /usr/local/bin/amc ] || [ -x /usr/bin/amc ]; } >/dev/null && echo installed\n" +
      "for d in \"$HOME/.config/systemd/user\" /etc/systemd/user /usr/lib/systemd/user; do [ -e \"$d/agent-mission-control.service\" ] && echo unit; done\n" +
      "[ -S \"$1/api.sock\" ] && echo socket; true",
      "sh", api.runtimeDir]
    stdout: StdioCollector {
      onStreamFinished: {
        var found = text.split("\n")
        api.installed = found.indexOf("installed") >= 0
        api.unit = found.indexOf("unit") >= 0
        api.socket = found.indexOf("socket") >= 0
        api.probed = true
      }
    }
  }

  // At most every 10s: the daemon may stay down all day (hideWhenDown), and
  // a process per poll is too much for that.
  function _probeDisk() {
    var now = Date.now()
    if (_probe.running || (probed && now - _probedAt < 10000)) return
    _probedAt = now
    _probe.running = true
  }

  // Why the daemon doesn't answer. Mirrors daemonState in ui/gnome/model.js
  // (tested there in gjs, and here by MC_TEST=states in test/shell.qml), plus
  // a "checking" state until the first disk probe is back. Returns {state,
  // summary, hint, action}; action is {label, kind: start|restart|copy|open,
  // arg} or null.
  function daemonState(o) {
    var start = "systemctl --user start " + service
    var restart = "systemctl --user restart " + service
    var logs = "Logs: journalctl --user -u " + service
    var restartAction = { label: "Restart daemon", kind: "restart", arg: restart }
    if (o.status === 200) return { state: "up", summary: "", hint: "", action: null }
    if (o.status === 401) return { state: "unauthorized", summary: "daemon refused the token",
      hint: "The token in " + o.tokenPath + " doesn't match the daemon at " + o.url + ". Every start writes a fresh one:\n" + restart,
      action: restartAction }
    if (o.status) return { state: "error", summary: "daemon returned " + o.status, hint: logs, action: restartAction }
    // A daemon started (it wrote the token) but nothing answers on TCP.
    if (o.token) return { state: "unreachable", summary: "daemon not answering at " + o.url,
      hint: "It may have crashed, or listen elsewhere (serve -listen).\n" + restart + "\n" + logs,
      action: restartAction }
    if (!o.probed) return { state: "checking", summary: "daemon not reachable", hint: "", action: null }
    if (o.socket) return { state: "no-token", summary: "can't read the daemon's token",
      hint: "Expected " + o.tokenPath + " (rewritten on every start, readable only by you). A restart writes it:\n" + restart,
      action: restartAction }
    if (!o.installed && !o.unit) return { state: "not-installed", summary: "amc is not installed",
      hint: "Install your distro package or a release from\n" + releases + "\nthen run: amc install --enable",
      action: { label: "Open releases", kind: "open", arg: releases } }
    if (!o.unit) return { state: "not-set-up", summary: "amc is installed but not set up",
      hint: "Run: amc install --enable\n(installs and starts the user service)",
      action: { label: "Copy command", kind: "copy", arg: "amc install --enable" } }
    return { state: "not-running", summary: "daemon is not running",
      hint: "Start it: " + start + "\n" + logs,
      action: { label: "Start daemon", kind: "start", arg: start } }
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
    var hadToken = !!token
    request("GET", "/workflows", undefined, function(status, data) {
      _inFlight = false
      lastHadToken = hadToken
      lastStatus = status === 200 && !data ? 502 : status // 200 with no JSON: not the daemon
      loaded = true
      if (status !== 200 || !data) {
        reachable = false
        if (status === 0 && !hadToken) _probeDisk()
        error = down.summary
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
