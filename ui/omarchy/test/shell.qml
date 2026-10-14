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
      }
    }
    readonly property var w: loader.item

    Timer {
      interval: 2500
      property var w: loader.item
      running: true
      onTriggered: {
        console.log("summary:", w.summary)
        for (var i = 0; i < w.api.workflows.length; i++) {
          var wf = w.api.workflows[i]
          console.log("  ", wf.status, wf.title, (wf.pending || []).length + " pending")
        }
        w.open()
        var choice = Quickshell.env("MC_ANSWER")
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
