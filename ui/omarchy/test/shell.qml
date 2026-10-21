import QtQuick
import Quickshell

// Headless harness for the bar widget, outside omarchy-shell:
//
//   QT_QPA_PLATFORM=offscreen qs -p ui/omarchy/test
//
// It loads Widget.qml the way omarchy-shell loads a third-party plugin (a
// Loader on a file URL outside the shell's config folder, so no qs.* or
// relative imports), with a fake bar facade (the same surface
// omarchy-shell's PluginBarApi gives third-party widgets), opens the panel
// and exits. Environment:
//   MC_URL     daemon API (default http://127.0.0.1:4200)
//   MC_SHOT    write a PNG of the open panel here
//   MC_ANSWER  answer the first pending ask with this choice, via the widget
//              ("a|b": several picks for a multi-select question)
//   MC_TEST=states  check Api.daemonState's classification table, print
//              "states: all passed" or the failures, and quit
//   MC_EXPECT_STATE  print FAIL and quit unless the daemon-down state is this
//              (e.g. XDG_RUNTIME_DIR set to an empty dir: not-running).
// qs exits 0 either way (Qt.exit has no receiver in Quickshell): grep.
ShellRoot {
  QtObject {
    id: fakeBar
    property color foreground: "#cdd6f4"
    property color background: "#1e1e2e"
    property color urgent: "#f38ba8"
    property string fontFamily: "JetBrainsMono Nerd Font"
    property string position: "top"
    property int barSize: 26
    property var activePopout: null
    function run(c) { console.log("bar.run:", c) }
    function showTooltip(t, s) {}
    function hideTooltip(t) {}
    function requestPopout(o) { activePopout = o }
    function releasePopout(o) { if (activePopout === o) activePopout = null }
  }

  FloatingWindow {
    id: harness
    implicitWidth: 600
    implicitHeight: 30
    color: "#11111b"

    Loader {
      id: loader
      anchors.right: parent.right
      // An absolute file URL, as PluginRegistry.entryPointUrl builds it. (A
      // relative "../" resolves inside the config scheme and is blackholed.)
      source: "file://" + (Quickshell.env("MC_WIDGET_DIR") || Quickshell.shellDir + "/..") + "/Widget.qml"
      onStatusChanged: if (status === Loader.Error) { console.log("LOAD FAILED:", Qt.createComponent(source).errorString()); Qt.quit() }
      onLoaded: {
        item.bar = fakeBar
        item.settings = { apiUrl: Quickshell.env("MC_URL") || "http://127.0.0.1:4200", notify: false }
        if (Quickshell.env("MC_TEST") === "states") { harness.checkStates(item.api); Qt.quit() }
      }
    }

    // Same table as ui/gnome/test/model.test.js, plus "checking".
    function checkStates(api) {
      var local = { probed: true, installed: true, unit: true, socket: false, url: "http://127.0.0.1:4200", tokenPath: "/run/user/1/agent-mission-control/token" }
      var cases = [
        [{ status: 200, token: true }, "up", null],
        [{ status: 401, token: true }, "unauthorized", "restart"],
        [{ status: 500, token: true }, "error", "restart"],
        [{ status: 0, token: true }, "unreachable", "restart"],
        [{ status: 0, token: false, probed: false }, "checking", null],
        [{ status: 0, token: false, socket: true }, "no-token", "restart"],
        [{ status: 0, token: false, installed: false, unit: false }, "not-installed", "open"],
        [{ status: 0, token: false, unit: false }, "not-set-up", "copy"],
        [{ status: 0, token: false, installed: false }, "not-running", "start"],
        [{ status: 0, token: false }, "not-running", "start"],
      ]
      var failed = 0
      for (var i = 0; i < cases.length; i++) {
        var o = Object.assign({}, local, cases[i][0])
        var d = api.daemonState(o)
        var kind = d.action ? d.action.kind : null
        if (d.state !== cases[i][1] || kind !== cases[i][2]) {
          failed++
          console.log("FAIL daemon state", JSON.stringify(cases[i][0]), "->", d.state, kind)
        }
      }
      var nr = api.daemonState(Object.assign({}, local, { status: 0, token: false }))
      if (nr.hint.indexOf("journalctl --user -u agent-mission-control.service") < 0) { failed++; console.log("FAIL not running: no logs hint") }
      console.log(failed ? failed + " failure(s)" : "states: all passed")
      return failed === 0
    }
    readonly property var w: loader.item

    Timer {
      interval: 2500
      property var w: loader.item
      running: true
      onTriggered: {
        console.log("summary:", w.summary)
        console.log("daemon state:", w.api.down.state)
        var expect = Quickshell.env("MC_EXPECT_STATE")
        if (expect && w.api.down.state !== expect) {
          console.log("FAIL expected daemon state", expect)
          Qt.quit()
          return
        }
        for (var i = 0; i < w.api.workflows.length; i++) {
          var wf = w.api.workflows[i]
          console.log("  ", wf.status, wf.title, (wf.pending || []).length + " pending")
        }
        w.open()
        var choice = Quickshell.env("MC_ANSWER")
        if (choice && choice.indexOf("|") >= 0) choice = choice.split("|")
        var target = w.api.workflows.filter(function(x) { return x.pending && x.pending.length })[0]
        if (choice && target) {
          w.api.answer(target.source_id, target.pending[0].id, choice, function(ok, err) {
            console.log("answer", choice, "->", ok ? "ok" : "error: " + err)
          })
        }
        shot.start()
      }
    }

    Timer {
      id: shot
      interval: 800
      property var w: loader.item
      onTriggered: {
        var path = Quickshell.env("MC_SHOT")
        if (!path) { Qt.quit(); return }
        w.panelCard.grabToImage(function(r) {
          console.log("screenshot", r.saveToFile(path) ? "saved " + path : "FAILED")
          Qt.quit()
        })
      }
    }
  }
}
