import QtQuick
import Quickshell
import Quickshell.Hyprland

// Mission control bar widget for omarchy-shell: a bar icon that counts the
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
  function open() { opened = true }
  function close() { opened = false }
  function toggle() { opened = !opened }

  Api {
    id: api
    baseUrl: root.setting("apiUrl", "http://127.0.0.1:4200")
    // Snappier while you are looking at it.
    interval: root.opened ? 1000 : root.setting("pollIntervalMs", 2000)
    onNeedsYou: function(workflow) {
      if (!root.setting("notify", true)) return
      var title = workflow.title || workflow.source_id
      var ask = workflow.pending && workflow.pending.length ? workflow.pending[0] : null
      var body = ask && ask.questions && ask.questions.length ? ask.questions[0].question : workflow.message
      var heading = { needs_input: "Agent needs you", stuck: "Agent is stuck", failed: "Agent failed" }[workflow.status]
      var icon = workflow.status === "needs_input" ? "dialog-question" : "dialog-warning"
      // argv, not a shell string: titles and questions come from agents.
      Quickshell.execDetached(["notify-send", "-a", "Mission control", "-i", icon,
        heading + ": " + title, String(body || "")])
    }
  }

  Timer {
    interval: 15000
    running: true
    repeat: true
    onTriggered: root.nowMs = Date.now()
  }

  // ------------------------------------------------------------ formatting

  function statusGlyph(s) {
    switch (s) {
    case "needs_input": return "" // question-circle
    case "running": return ""     // spinner
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

    Text {
      id: icon
      anchors.verticalCenter: parent.verticalCenter
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

    Text {
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
    onEntered: if (root.bar && !root.opened) root.bar.showTooltip(root, "Mission control: " + root.summary)
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
          text: "Mission control"
          color: root.fg
          font.family: root.fontFamily
          font.pixelSize: 15
          font.bold: true
        }
        Text {
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
            visible: api.reachable && api.workflows.length === 0
            width: parent.width
            text: "No workflows yet. Start an agent with `opencode --port N` and `mission-control serve -discover`."
            wrapMode: Text.Wrap
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: 12
          }

          Text {
            visible: api.loaded && !api.reachable
            width: parent.width
            text: "Start the daemon: mission-control serve -discover\n(looking for it at " + api.baseUrl + ")"
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

    function report(ok, err, okText) {
      busy = false
      feedbackIsError = !ok
      feedback = ok ? okText : (err || "failed")
    }
    function choose(choice) {
      busy = true
      feedback = "sending…"
      api.answer(workflow.source_id, ask.id, choice, function(ok, err) { wcard.report(ok, err, "answered: " + choice) })
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
        implicitHeight: titleText.implicitHeight

        Text {
          id: glyph
          anchors.verticalCenter: parent.verticalCenter
          text: root.statusGlyph(wcard.workflow.status)
          color: root.statusColor(wcard.workflow)
          font.family: root.fontFamily
          font.pixelSize: 13
        }
        Text {
          id: titleText
          anchors.left: glyph.right
          anchors.leftMargin: 8
          anchors.right: ageText.left
          anchors.rightMargin: 8
          text: wcard.workflow.title || wcard.workflow.source_id
          color: root.fg
          font.family: root.fontFamily
          font.pixelSize: 13
          font.bold: true
          elide: Text.ElideRight
        }
        Text {
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
        width: parent.width
        visible: text !== ""
        text: root.tilde(wcard.workflow.location)
        color: root.dim
        font.family: root.fontFamily
        font.pixelSize: 11
        elide: Text.ElideMiddle
      }

      Text {
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
          onActivated: wcard.choose(Object.keys(wcard.picked).join(","))
        }
      }

      InputLine {
        width: parent.width
        visible: !!wcard.question && !!wcard.question.free_text && wcard.canInput
        placeholder: "or type an answer, Enter to send"
        onSubmitted: function(t) { text = ""; wcard.choose(t) }
      }

      Text {
        width: parent.width
        visible: !!wcard.ask && !wcard.question
        text: "This ask has several questions; answer it with `mission-control show " + String(wcard.workflow.source_id).slice(-8) + "`."
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
