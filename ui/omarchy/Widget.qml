import QtQuick
import Quickshell
import Quickshell.Hyprland
import Quickshell.Io

// Agent Mission Control bar widget for omarchy-shell: a bar icon that counts the
// workflows waiting on you, and a panel listing every workflow with inline
// answers. Self-contained on purpose: it uses only QtQuick, Quickshell and
// the injected `bar` facade (colors, font, run, popout coordination), not
// the shell's private qs.* modules, so it keeps working across shell updates.
Item {
  id: root

  // Injected by the bar.
  property var bar: null
  property string moduleName: ""
  property var settings: ({})

  function setting(name, fallback) {
    var v = settings ? settings[name] : undefined
    return v === undefined || v === null ? fallback : v
  }

  readonly property color fg: bar ? bar.foreground : "#d8dee9"
  readonly property color bg: bar ? bar.background : "#1e1e2e"
  readonly property color urgent: bar ? bar.urgent : "#e06c75"
  readonly property color dim: alpha(fg, 0.55)
  readonly property color faint: alpha(fg, 0.12)
  readonly property string fontFamily: bar && bar.fontFamily ? bar.fontFamily : "monospace"
  readonly property string home: Quickshell.env("HOME") || ""

  property bool opened: false
  property double nowMs: Date.now()

  // For scripting and tests (omarchy-shell IPC, the harness in ui/omarchy/test).
  readonly property alias api: api
  readonly property alias panelCard: card

  function alpha(c, a) { return Qt.rgba(c.r, c.g, c.b, a) }
  function escapeMarkup(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;") }
  function open() { opened = true }
  function close() { opened = false }
  function toggle() { opened = !opened }

  // `omarchy-shell agent-mission-control toggle` (e.g. from a Hyprland keybind),
  // like the shell's own widgets. With a bar on several monitors the last
  // one loaded gets the target.
  IpcHandler {
    target: "agent-mission-control"
    function open(): void { root.open() }
    function close(): void { root.close() }
    function toggle(): void { root.toggle() }
  }

  // The daemon writes a fresh token on every start; only this user can read it.
  FileView {
    id: tokenFile
    path: (Quickshell.env("XDG_RUNTIME_DIR") || "") + "/agent-mission-control/token"
    watchChanges: true
    onFileChanged: reload()
  }

  Api {
    id: api
    baseUrl: root.setting("apiUrl", "http://127.0.0.1:4200")
    token: tokenFile.loaded ? tokenFile.text().trim() : ""
    // Snappier while you are looking at it.
    interval: root.opened ? 1000 : root.setting("pollIntervalMs", 2000)
    onNeedsYou: function(workflow) {
      if (!root.setting("notify", true)) return
      var title = workflow.title || workflow.source_id
      var ask = workflow.pending && workflow.pending.length ? workflow.pending[0] : null
      var body = ask && ask.questions && ask.questions.length ? ask.questions[0].question : workflow.message
      var heading = { needs_input: "Agent needs you", stuck: "Agent is stuck", failed: "Agent failed" }[workflow.status]
      var icon = workflow.status === "needs_input" ? "dialog-question" : "dialog-warning"
      // argv, not a shell string, and markup escaped: titles and questions
      // come from agents, and notification bodies may be rendered as markup.
      Quickshell.execDetached(["notify-send", "-a", "Agent Mission Control", "-i", icon,
        root.escapeMarkup(heading + ": " + title), root.escapeMarkup(String(body || ""))])
    }
  }

  Timer {
    interval: 15000
    running: true
    repeat: true
    onTriggered: root.nowMs = Date.now()
  }

  // ---------------------------------------------------- focus the terminal

  // Each workflow carries `pids`: the agent process and its ancestors
  // (agent → shell → terminal emulator). The nearest one that owns a
  // Hyprland window is the agent's terminal.
  property var _focusChain: []

  function focusTerminal(pids) {
    if (!pids || !pids.length || focusProc.running) return
    _focusChain = pids
    focusProc.running = true
  }

  Process {
    id: focusProc
    command: ["hyprctl", "clients", "-j"]
    stdout: StdioCollector {
      onStreamFinished: {
        var clients = []
        try { clients = JSON.parse(text) } catch (e) { return }
        for (var i = 0; i < root._focusChain.length; i++) {
          for (var j = 0; j < clients.length; j++) {
            if (clients[j].pid === root._focusChain[i]) {
              Quickshell.execDetached(["hyprctl", "dispatch", "focuswindow", "address:" + clients[j].address])
              root.close()
              return
            }
          }
        }
      }
    }
  }

  // ------------------------------------------------------------ formatting

  function statusGlyph(s) {
    switch (s) {
    case "needs_input": return "" // question-circle
    case "running": return "●··"   // first frame of the Larson scanner
    case "stuck": return ""       // pause-circle
    case "failed": return ""      // times-circle
    case "done": return ""        // check-circle
    }
    return ""
  }

  function statusColor(w) {
    if (w.stale) return dim
    switch (w.status) {
    case "needs_input": case "stuck": case "failed": return urgent
    case "done": return dim
    }
    return fg
  }

  function statusLabel(w) {
    var s = (w.status || "unknown").replace("_", " ")
    return w.stale ? s + " · stale" : s
  }

  // The daemon emits RFC 3339 with nanoseconds; JS dates take milliseconds.
  function parseTime(t) {
    return Date.parse(String(t || "").replace(/(\.\d{3})\d+/, "$1"))
  }

  function age(t) {
    var ms = parseTime(t)
    if (isNaN(ms)) return ""
    var s = Math.max(0, Math.round((nowMs - ms) / 1000))
    if (s < 60) return "now"
    if (s < 3600) return Math.floor(s / 60) + "m"
    if (s < 86400) return Math.floor(s / 3600) + "h"
    return Math.floor(s / 86400) + "d"
  }

  function tilde(p) {
    if (!p) return ""
    return home && p.indexOf(home) === 0 ? "~" + p.slice(home.length) : p
  }

  readonly property string summary: {
    if (!api.reachable) return api.loaded ? api.error : "connecting…"
    var parts = []
    if (api.waitingCount) parts.push(api.waitingCount + " waiting on you")
    if (api.stuckCount) parts.push(api.stuckCount + " stuck")
    if (api.failedCount) parts.push(api.failedCount + " failed")
    if (api.runningCount) parts.push(api.runningCount + " running")
    if (!parts.length) parts.push(api.workflows.length ? "nothing needs you" : "no workflows")
    return parts.join(" · ")
  }

  // --------------------------------------------------------------- bar icon

  visible: !(setting("hideWhenDown", false) && api.loaded && !api.reachable)
  implicitWidth: iconRow.implicitWidth + 12
  implicitHeight: bar && bar.barSize ? bar.barSize : 26

  // Another widget opened its popup: get out of the way.
  Connections {
    target: root.bar
    ignoreUnknownSignals: true
    function onActivePopoutChanged() {
      if (root.opened && root.bar.activePopout && root.bar.activePopout !== root) root.close()
    }
  }

  onOpenedChanged: {
    if (!bar) return
    if (opened) { nowMs = Date.now(); api.refresh(); bar.requestPopout(root) }
    else if (bar.activePopout === root) bar.releasePopout(root)
  }

  Row {
    id: iconRow
    anchors.centerIn: parent
    spacing: 4

    Item {
      id: iconMark
      width: 22
      height: 22
      anchors.verticalCenter: parent.verticalCenter

      Canvas {
        id: runOrbit
        anchors.fill: parent
        visible: api.reachable && api.runningCount > 0
        property color ink: !api.reachable ? root.dim : (api.attentionCount ? root.urgent : root.fg)
        onInkChanged: requestPaint()
        onVisibleChanged: requestPaint()
        onPaint: {
          var ctx = getContext("2d")
          ctx.clearRect(0, 0, width, height)
          var cx = width / 2
          var cy = height / 2
          var radius = 9.5
          var end = Math.PI * 0.65
          ctx.strokeStyle = ink
          ctx.lineWidth = 1.3
          ctx.lineCap = "round"
          ctx.beginPath()
          ctx.arc(cx, cy, radius, -Math.PI * 0.85, end, false)
          ctx.stroke()
          ctx.fillStyle = ink
          ctx.beginPath()
          ctx.arc(cx + radius * Math.cos(end), cy + radius * Math.sin(end), 1.5, 0, Math.PI * 2)
          ctx.fill()
        }

        RotationAnimation on rotation {
          from: 0
          to: 360
          duration: 1000
          loops: Animation.Infinite
          running: runOrbit.visible
        }
      }

      Text {
        textFormat: Text.PlainText // agent text: never HTML
        id: icon
        anchors.centerIn: parent
        text: "󰚚" // nf-md-robot_outline
        font.family: root.fontFamily
        font.pixelSize: 14
        color: !api.reachable ? root.dim : (api.attentionCount ? root.urgent : root.fg)

        SequentialAnimation on opacity {
          running: api.waitingCount > 0
          loops: Animation.Infinite
          alwaysRunToEnd: true
          NumberAnimation { to: 0.35; duration: 900; easing.type: Easing.InOutSine }
          NumberAnimation { to: 1.0; duration: 900; easing.type: Easing.InOutSine }
        }
      }
    }

    Text {
      textFormat: Text.PlainText // agent text: never HTML
      anchors.verticalCenter: parent.verticalCenter
      visible: text !== ""
      // What needs you; otherwise how many are busy.
      text: api.attentionCount ? String(api.attentionCount) : (api.runningCount ? String(api.runningCount) : "")
      font.family: root.fontFamily
      font.pixelSize: 12
      font.bold: api.attentionCount > 0
      color: api.attentionCount ? root.urgent : root.dim
    }
  }

  MouseArea {
    id: hitArea
    anchors.fill: parent
    hoverEnabled: true
    acceptedButtons: Qt.LeftButton | Qt.RightButton
    cursorShape: Qt.PointingHandCursor
    onClicked: function(mouse) {
      if (mouse.button === Qt.RightButton) api.refresh()
      else root.toggle()
    }
    onEntered: if (root.bar && !root.opened) root.bar.showTooltip(root, "Agent Mission Control: " + root.summary)
    onExited: if (root.bar) root.bar.hideTooltip(root)
  }

  // ------------------------------------------------------------------ panel

  PopupWindow {
    id: popup

    readonly property int margin: 8
    readonly property var anchorWindow: root.QsWindow.window

    visible: root.opened
    color: "transparent"
    implicitWidth: 440
    implicitHeight: Math.min(card.contentHeightWanted, 640)

    anchor {
      id: popupAnchor
      window: popup.anchorWindow
      adjustment: PopupAdjustment.Slide
      edges: Edges.Top | Edges.Left
      gravity: Edges.Bottom | Edges.Right
      rect.width: 1
      rect.height: 1

      onAnchoring: {
        var w = popup.anchorWindow
        if (!w) return
        var pos = root.bar ? root.bar.position : "top"
        var x = root.width / 2 - popup.implicitWidth / 2
        var y = root.height + popup.margin
        if (pos === "bottom") y = -popup.implicitHeight - popup.margin
        else if (pos === "left") { x = root.width + popup.margin; y = 0 }
        else if (pos === "right") { x = -popup.implicitWidth - popup.margin; y = 0 }
        var p = w.contentItem.mapFromItem(root, x, y)
        if (pos === "top" || pos === "bottom")
          p.x = Math.max(popup.margin, Math.min(p.x, w.width - popup.implicitWidth - popup.margin))
        popupAnchor.rect.x = Math.round(p.x)
        popupAnchor.rect.y = Math.round(p.y)
      }
    }

    HyprlandFocusGrab {
      active: root.opened
      windows: popup.anchorWindow ? [popup, popup.anchorWindow] : [popup]
      onCleared: root.close()
    }

    Rectangle {
      id: card
      anchors.fill: parent
      radius: 10
      color: root.bg
      border.color: root.alpha(root.fg, 0.18)
      border.width: 1

      readonly property int pad: 14
      readonly property real contentHeightWanted: header.implicitHeight + body.contentHeight + pad * 3

      focus: true
      Keys.onEscapePressed: root.close()

      Column {
        id: header
        x: card.pad
        y: card.pad
        width: card.width - card.pad * 2
        spacing: 2

        Text {
          textFormat: Text.PlainText // agent text: never HTML
          text: "Agent Mission Control"
          color: root.fg
          font.family: root.fontFamily
          font.pixelSize: 15
          font.bold: true
        }
        Text {
          textFormat: Text.PlainText // agent text: never HTML
          width: parent.width
          text: root.summary
          color: api.reachable ? root.dim : root.urgent
          font.family: root.fontFamily
          font.pixelSize: 12
          elide: Text.ElideRight
        }
      }

      Flickable {
        id: body
        x: card.pad
        anchors.top: header.bottom
        anchors.topMargin: card.pad
        anchors.bottom: parent.bottom
        anchors.bottomMargin: card.pad
        width: card.width - card.pad * 2
        contentHeight: list.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds

        Column {
          id: list
          width: body.width
          spacing: 8

          Text {
            textFormat: Text.PlainText // agent text: never HTML
            visible: api.reachable && api.workflows.length === 0
            width: parent.width
            text: "No workflows yet. Start an agent with `opencode --port N` and `amc serve -discover`."
            wrapMode: Text.Wrap
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: 12
          }

          Text {
            textFormat: Text.PlainText // agent text: never HTML
            visible: api.loaded && !api.reachable
            width: parent.width
            text: "Start the daemon: amc serve -discover\n(looking for it at " + api.baseUrl + ")"
            wrapMode: Text.Wrap
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: 12
          }

          Repeater {
            model: api.workflows
            delegate: WorkflowCard {
              required property var modelData
              workflow: modelData
              width: list.width
            }
          }
        }
      }
    }
  }

  // ------------------------------------------------------------ components

  component Chip: Rectangle {
    id: chip
    property string label: ""
    property bool selected: false
    property bool emphasized: false
    signal activated()

    implicitWidth: chipText.implicitWidth + 18
    implicitHeight: chipText.implicitHeight + 10
    radius: 6
    color: chipMouse.containsMouse || selected ? root.alpha(root.fg, 0.22) : root.faint
    border.color: emphasized ? root.urgent : (selected ? root.fg : "transparent")
    border.width: 1

    Text {
      textFormat: Text.PlainText // agent text: never HTML
      id: chipText
      anchors.centerIn: parent
      text: chip.label
      color: root.fg
      font.family: root.fontFamily
      font.pixelSize: 12
    }
    MouseArea {
      id: chipMouse
      anchors.fill: parent
      hoverEnabled: true
      cursorShape: Qt.PointingHandCursor
      onClicked: chip.activated()
    }
  }

  component InputLine: Rectangle {
    id: line
    property string placeholder: ""
    property alias text: input.text
    signal submitted(string text)

    implicitHeight: input.implicitHeight + 12
    radius: 6
    color: root.faint
    border.color: input.activeFocus ? root.alpha(root.fg, 0.5) : "transparent"
    border.width: 1

    TextInput {
      id: input
      anchors.fill: parent
      anchors.margins: 6
      color: root.fg
      font.family: root.fontFamily
      font.pixelSize: 12
      clip: true
      selectByMouse: true
      onAccepted: if (text.trim() !== "") line.submitted(text.trim())
      Keys.onEscapePressed: root.close()

      Text {
        textFormat: Text.PlainText // agent text: never HTML
        visible: !input.text && !input.activeFocus
        text: line.placeholder
        color: root.dim
        font: input.font
      }
    }
  }

  component WorkflowCard: Rectangle {
    id: wcard
    property var workflow: ({})
    readonly property var ask: workflow.pending && workflow.pending.length ? workflow.pending[0] : null
    readonly property var question: ask && ask.questions && ask.questions.length === 1 ? ask.questions[0] : null
    readonly property bool canInput: workflow.capabilities && workflow.capabilities.can_receive_input && !workflow.stale
    readonly property bool canContinue: canInput && !ask && workflow.status !== "running"
    readonly property bool needsYou: !workflow.stale && (workflow.status === "needs_input" || workflow.status === "stuck" || workflow.status === "failed")
    property var picked: ({})
    property string feedback: ""
    property bool feedbackIsError: false
    property bool busy: false
    property int scannerStep: 0
    readonly property var scannerFrames: ["●··", "·●·", "··●", "·●·"]

    Timer {
      interval: 180
      repeat: true
      running: root.opened && wcard.workflow.status === "running" && !wcard.workflow.stale
      onTriggered: wcard.scannerStep = (wcard.scannerStep + 1) % wcard.scannerFrames.length
      onRunningChanged: if (!running) wcard.scannerStep = 0
    }

    function report(ok, err, okText) {
      busy = false
      feedbackIsError = !ok
      feedback = ok ? okText : (err || "failed")
    }
    function choose(choice) {
      busy = true
      feedback = "sending…"
      api.answer(workflow.source_id, ask.id, choice, function(ok, err) { wcard.report(ok, err, "answered: " + [].concat(choice).join(", ")) })
    }
    function togglePick(opt) {
      var p = Object.assign({}, picked)
      if (p[opt]) delete p[opt]; else p[opt] = true
      picked = p
    }

    // A new ask (or none) invalidates any half-made multi-select.
    onAskChanged: { picked = ({}); if (!busy) feedback = "" }

    implicitHeight: content.implicitHeight + 20
    radius: 8
    color: needsYou ? root.alpha(root.urgent, 0.10) : root.alpha(root.fg, 0.05)
    border.color: needsYou ? root.alpha(root.urgent, workflow.status === "needs_input" ? 0.45 : 0.25) : "transparent"
    border.width: 1
    opacity: workflow.stale ? 0.6 : 1

    Column {
      id: content
      x: 10
      y: 10
      width: wcard.width - 20
      spacing: 5

      Item {
        width: parent.width
        implicitHeight: Math.max(titleText.implicitHeight, termChip.implicitHeight)

        Text {
          textFormat: Text.PlainText // agent text: never HTML
          id: glyph
          anchors.verticalCenter: parent.verticalCenter
          text: wcard.workflow.status === "running"
            ? wcard.scannerFrames[wcard.scannerStep] : root.statusGlyph(wcard.workflow.status)
          color: root.statusColor(wcard.workflow)
          font.family: root.fontFamily
          font.pixelSize: 13
        }
        Text {
          textFormat: Text.PlainText // agent text: never HTML
          id: titleText
          anchors.left: glyph.right
          anchors.leftMargin: 8
          anchors.right: termChip.visible ? termChip.left : ageText.left
          anchors.rightMargin: 8
          text: wcard.workflow.title || wcard.workflow.source_id
          color: root.fg
          font.family: root.fontFamily
          font.pixelSize: 13
          font.bold: true
          elide: Text.ElideRight
        }
        Chip {
          id: termChip
          anchors.right: ageText.left
          anchors.rightMargin: 8
          anchors.verticalCenter: parent.verticalCenter
          visible: !!wcard.workflow.pids && wcard.workflow.pids.length > 0 && !wcard.workflow.stale
          label: "\uf120" // terminal
          implicitWidth: visible ? 26 : 0
          implicitHeight: 20
          onActivated: root.focusTerminal(wcard.workflow.pids)
        }
        Text {
          textFormat: Text.PlainText // agent text: never HTML
          id: ageText
          anchors.right: parent.right
          anchors.verticalCenter: parent.verticalCenter
          text: root.statusLabel(wcard.workflow) + " · " + root.age(wcard.workflow.updated_at)
          color: root.statusColor(wcard.workflow)
          font.family: root.fontFamily
          font.pixelSize: 11
        }
      }

      Text {
        textFormat: Text.PlainText // agent text: never HTML
        width: parent.width
        visible: text !== ""
        text: root.tilde(wcard.workflow.location)
        color: root.dim
        font.family: root.fontFamily
        font.pixelSize: 11
        elide: Text.ElideMiddle
      }

      Text {
        textFormat: Text.PlainText // agent text: never HTML
        width: parent.width
        visible: !wcard.question && text !== ""
        text: wcard.workflow.stale ? (wcard.workflow.stale_reason || "source unreachable") : (wcard.workflow.message || "")
        color: root.dim
        font.family: root.fontFamily
        font.pixelSize: 12
        wrapMode: Text.Wrap
        maximumLineCount: 3
        elide: Text.ElideRight
      }

      // ---- waiting on you
      Text {
        textFormat: Text.PlainText // agent text: never HTML
        width: parent.width
        visible: !!wcard.question
        text: wcard.question ? ((wcard.question.header ? wcard.question.header + ": " : "") + wcard.question.question) : ""
        color: root.fg
        font.family: root.fontFamily
        font.pixelSize: 12
        wrapMode: Text.Wrap
      }

      Flow {
        width: parent.width
        spacing: 6
        visible: !!wcard.question && wcard.canInput
        Repeater {
          model: wcard.question ? (wcard.question.options || []) : []
          delegate: Chip {
            required property string modelData
            label: modelData
            selected: !!wcard.picked[modelData]
            emphasized: wcard.ask && wcard.ask.kind === "permission" && modelData === "reject"
            enabled: !wcard.busy
            onActivated: wcard.question.multiple ? wcard.togglePick(modelData) : wcard.choose(modelData)
          }
        }
        Chip {
          visible: !!wcard.question && !!wcard.question.multiple
          label: "Send selected"
          enabled: !wcard.busy && Object.keys(wcard.picked).length > 0
          opacity: enabled ? 1 : 0.5
          onActivated: wcard.choose(wcard.question.options.filter(function(o) { return !!wcard.picked[o] }))
        }
      }

      // Observe-only sources (Claude Code): show the choices, answer there.
      Text {
        textFormat: Text.PlainText // agent text: never HTML
        width: parent.width
        visible: !wcard.canInput && !wcard.workflow.stale && (!!wcard.question || (!wcard.ask && wcard.workflow.status === "needs_input"))
        text: (wcard.question && wcard.question.options && wcard.question.options.length
                 ? "Options: " + wcard.question.options.join(" · ") + "\n" : "")
              + "Answer in its terminal" + (wcard.workflow.pids && wcard.workflow.pids.length ? " (\uf120 jumps there)" : "") + "."
        color: root.dim
        font.family: root.fontFamily
        font.pixelSize: 11
        wrapMode: Text.Wrap
      }

      InputLine {
        width: parent.width
        visible: !!wcard.question && !!wcard.question.free_text && wcard.canInput
        placeholder: "or type an answer, Enter to send"
        onSubmitted: function(t) { text = ""; wcard.choose(t) }
      }

      Text {
        textFormat: Text.PlainText // agent text: never HTML
        width: parent.width
        visible: !!wcard.ask && !wcard.question && !wcard.workflow.stale
        // Neither the panel nor the CLI can answer a multi-question ask.
        text: (wcard.ask && wcard.ask.questions ? wcard.ask.questions.map(function(q) { return "• " + q.question }).join("\n") + "\n" : "")
              + "Answer in its terminal" + (wcard.workflow.pids && wcard.workflow.pids.length ? " (\uf120 jumps there)" : "") + "."
        color: root.dim
        font.family: root.fontFamily
        font.pixelSize: 11
        wrapMode: Text.Wrap
      }

      // ---- finished, stuck or failed: tell it how to continue
      InputLine {
        width: parent.width
        visible: wcard.canContinue
        placeholder: wcard.workflow.status === "done" ? "follow up…"
          : wcard.workflow.status === "needs_input" ? "reply…" : "tell it how to continue…"
        onSubmitted: function(t) {
          text = ""
          wcard.busy = true
          wcard.feedback = "sending…"
          api.send(wcard.workflow.source_id, t, function(ok, err) { wcard.report(ok, err, "sent") })
        }
      }

      Text {
        textFormat: Text.PlainText // agent text: never HTML
        width: parent.width
        visible: wcard.feedback !== ""
        text: wcard.feedback
        color: wcard.feedbackIsError ? root.urgent : root.dim
        font.family: root.fontFamily
        font.pixelSize: 11
        wrapMode: Text.Wrap
      }
    }
  }
}
