import QtQuick
import Quickshell
import Quickshell.Io

// The promo scene: a mock Omarchy desktop (bar, three agent terminals)
// around the REAL bar widget (ui/omarchy/Widget.qml, loaded the way
// omarchy-shell loads it) talking to a REAL amc daemon, which watches the
// scripted fake OpenCode server and a Claude Code session played through the
// real hook command. The terminals, captions, cursor and notification toast
// are drawn here; the bar icon, its count and pulse, the panel, and the
// answer that reaches the agent are the real thing. The toast is drawn from
// what the widget passed to `notify-send` (a stub on PATH, see
// record-promo.sh), never the desktop's notification daemon.
//
// Runs offscreen (QT_QPA_PLATFORM=offscreen), so nothing appears on your
// screen, and writes every frame to $PROMO_FRAMES as f<index>-<ms>.png for
// record-promo.sh to assemble in real time. Environment:
//   MC_URL           the demo daemon's TCP API
//   MC_WIDGET_DIR    ui/omarchy
//   PROMO_FRAMES     where frames go
//   PROMO_CUE        promo/cue.sh, run at the story's beats
//   PROMO_NOTIFY     the file the notify-send stub writes
ShellRoot {
  id: shell

  readonly property color bgColor: "#1a1b26"
  readonly property color fgColor: "#c0caf5"
  readonly property color dimColor: "#565f89"
  readonly property color amber: "#e0af68"
  readonly property color blue: "#7aa2f7"
  readonly property color green: "#9ece6a"
  readonly property string mono: "JetBrainsMono Nerd Font"

  // The facade omarchy-shell gives third-party widgets (PluginBarApi).
  // urgent is the theme's urgent color; amber here.
  QtObject {
    id: fakeBar
    property color foreground: shell.fgColor
    property color background: shell.bgColor
    property color urgent: shell.amber
    property string fontFamily: shell.mono
    property string position: "top"
    property int barSize: 26
    property var activePopout: null
    function run(c) {}
    function showTooltip(t, s) {}
    function hideTooltip(t) {}
    function requestPopout(o) { activePopout = o }
    function releasePopout(o) { if (activePopout === o) activePopout = null }
  }

  // Terminal output for the mock tiles: [text, color] pairs.
  component Term: Rectangle {
    id: term
    property string header: ""
    property var lines: []
    property bool working: false
    property bool active: false
    property int spin: 0
    color: Qt.rgba(0.102, 0.106, 0.149, 0.94)
    border.width: 2
    border.color: active ? "#7aa2f7" : "#2f334d"
    clip: true

    Timer {
      interval: 90; repeat: true; running: term.working
      onTriggered: term.spin = (term.spin + 1) % 10
    }

    Column {
      x: 14; y: 12
      width: term.width - 28
      spacing: 3
      Text {
        text: term.header
        color: "#565f89"
        font.family: "JetBrainsMono Nerd Font"; font.pixelSize: 12
        bottomPadding: 6
      }
      Repeater {
        model: term.lines
        delegate: Text {
          required property var modelData
          width: parent.width
          text: modelData[0]
          color: modelData[1] || "#c0caf5"
          font.family: "JetBrainsMono Nerd Font"; font.pixelSize: 12
          wrapMode: Text.Wrap
        }
      }
      Text {
        visible: term.working
        text: "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"[term.spin] + " working…"
        color: "#7aa2f7"
        font.family: "JetBrainsMono Nerd Font"; font.pixelSize: 12
        topPadding: 4
      }
    }
  }

  component Card: Rectangle {
    // full-frame title / end card
    color: "#0d0e14"
    opacity: 0
    visible: opacity > 0
    Behavior on opacity { NumberAnimation { duration: 600; easing.type: Easing.InOutQuad } }
  }

  FloatingWindow {
    id: win
    implicitWidth: 1280
    implicitHeight: 720
    color: "black"

    // What gets recorded.
    Item {
      id: frame
      anchors.fill: parent
      clip: true

      Item {
        id: scene
        width: 1280
        height: 720
        property real zoom: 1
        Behavior on zoom { NumberAnimation { duration: 900; easing.type: Easing.InOutCubic } }
        transform: Scale { origin.x: 1280; origin.y: 0; xScale: scene.zoom; yScale: scene.zoom }

        // ------------------------------------------------------ wallpaper
        Rectangle {
          anchors.fill: parent
          gradient: Gradient {
            GradientStop { position: 0; color: "#1f2335" }
            GradientStop { position: 1; color: "#16161e" }
          }
        }

        // ------------------------------------------------------ terminals
        Term {
          id: termAuth
          x: 10; y: 36; width: 625; height: 674
          active: true
          header: "opencode  ~/src/api"
        }
        Term {
          id: termSync
          x: 645; y: 36; width: 625; height: 332
          header: "opencode  ~/src/sync"
        }
        Term {
          id: termClaude
          x: 645; y: 378; width: 625; height: 332
          header: "claude  ~/src/gateway"
        }

        // ------------------------------------------------------------ bar
        Rectangle {
          id: barRect
          width: parent.width
          height: 26
          color: shell.bgColor

          Row {
            x: 12
            anchors.verticalCenter: parent.verticalCenter
            spacing: 12
            Repeater {
              model: ["1", "2", "3", "4", "5"]
              delegate: Text {
                required property string modelData
                text: modelData
                color: modelData === "1" ? shell.fgColor : shell.dimColor
                font.family: shell.mono; font.pixelSize: 12
                font.bold: modelData === "1"
              }
            }
          }
          Text {
            anchors.centerIn: parent
            text: "Wednesday 14:32"
            color: shell.fgColor
            font.family: shell.mono; font.pixelSize: 12
          }
          Row {
            id: rightRow
            anchors.right: parent.right
            anchors.rightMargin: 10
            anchors.verticalCenter: parent.verticalCenter
            spacing: 10

            Loader {
              id: loader
              anchors.verticalCenter: parent.verticalCenter
              source: "file://" + Quickshell.env("MC_WIDGET_DIR") + "/Widget.qml"
              onStatusChanged: if (status === Loader.Error) { console.log("LOAD FAILED"); Qt.quit() }
              onLoaded: {
                item.bar = fakeBar
                item.settings = { apiUrl: Quickshell.env("MC_URL"), notify: true, pollIntervalMs: 500 }
              }
            }
            Repeater {
              model: ["", "", ""] // wifi, volume, battery
              delegate: Text {
                required property string modelData
                anchors.verticalCenter: parent.verticalCenter
                text: modelData
                color: shell.fgColor
                font.family: shell.mono; font.pixelSize: 13
              }
            }
          }
        }

        // -------------------------------------------- notification toast
        // Drawn from the arguments the widget gave notify-send.
        Rectangle {
          id: toast
          x: 1280 - width - 12
          y: 38
          width: 340
          height: toastCol.implicitHeight + 24
          color: shell.bgColor
          border.width: 2
          border.color: shell.amber
          opacity: 0
          visible: opacity > 0
          Behavior on opacity { NumberAnimation { duration: 250 } }
          property string summary: ""
          property string body: ""

          Column {
            id: toastCol
            x: 14; y: 12
            width: toast.width - 28
            spacing: 4
            Text {
              width: parent.width
              text: toast.summary
              color: shell.fgColor
              font.family: shell.mono; font.pixelSize: 13; font.bold: true
              wrapMode: Text.Wrap
            }
            Text {
              width: parent.width
              text: toast.body
              color: shell.fgColor
              font.family: shell.mono; font.pixelSize: 12
              wrapMode: Text.Wrap
            }
          }
          Timer { id: toastTimer; interval: 3600; onTriggered: toast.opacity = 0 }
          function show(s, b) { summary = s; body = b; opacity = 1; toastTimer.restart() }
        }

        FileView {
          path: Quickshell.env("PROMO_NOTIFY")
          watchChanges: true
          onFileChanged: reload()
          onLoaded: {
            var l = text().split("\n")
            if (l.length >= 2 && l[0] !== "") toast.show(shell.unmarkup(l[0]), shell.unmarkup(l[1]))
          }
        }

        // ------------------------------------------------ the real panel
        // The widget's popup is its own window; its card is grabbed every
        // frame and shown where omarchy-shell would place it.
        Image {
          id: panelImg
          x: 1280 - 8 - width
          y: 26 + 8
          width: loader.item ? loader.item.panelCard.width : 0
          height: loader.item ? loader.item.panelCard.height : 0
          visible: loader.item && loader.item.opened && source != ""
          smooth: true
          mipmap: true
        }

        // ------------------------------------------------------- cursor
        Item {
          id: cursor
          x: 980; y: 520
          opacity: 0
          z: 10
          Behavior on opacity { NumberAnimation { duration: 200 } }
          Behavior on x { NumberAnimation { id: mx; duration: 750; easing.type: Easing.InOutCubic } }
          Behavior on y { NumberAnimation { duration: 750; easing.type: Easing.InOutCubic } }

          Rectangle {
            id: ripple
            x: -width / 2; y: -height / 2
            width: 8; height: 8; radius: width / 2
            color: "transparent"
            border.color: "white"; border.width: 2
            opacity: 0
          }
          ParallelAnimation {
            id: clickAnim
            NumberAnimation { target: ripple; property: "width"; from: 6; to: 34; duration: 380 }
            NumberAnimation { target: ripple; property: "height"; from: 6; to: 34; duration: 380 }
            NumberAnimation { target: ripple; property: "opacity"; from: 0.9; to: 0; duration: 380 }
          }
          Canvas {
            width: 16; height: 24
            onPaint: {
              var c = getContext("2d")
              c.beginPath()
              c.moveTo(1, 1); c.lineTo(1, 19); c.lineTo(5.5, 15); c.lineTo(8.5, 22)
              c.lineTo(11, 21); c.lineTo(8, 14); c.lineTo(14, 14); c.closePath()
              c.fillStyle = "white"; c.fill()
              c.lineWidth = 1.2; c.strokeStyle = "black"; c.stroke()
            }
          }
          function moveTo(p) { x = p.x; y = p.y }
          function click() { clickAnim.restart() }
        }
      }

      // --------------------------------------------------------- captions
      Rectangle {
        id: caption
        x: 28
        y: 720 - height - 28
        width: capText.implicitWidth + 36
        height: capText.implicitHeight + 20
        radius: 8
        color: Qt.rgba(0.05, 0.05, 0.08, 0.88)
        border.color: Qt.rgba(1, 1, 1, 0.08)
        opacity: 0
        Behavior on opacity { NumberAnimation { duration: 300 } }
        Text {
          id: capText
          anchors.centerIn: parent
          color: "white"
          font.family: shell.mono; font.pixelSize: 24; font.bold: true
        }
        function say(t) {
          if (!t) { opacity = 0; return }
          capText.text = t; opacity = 1
        }
      }

      // ------------------------------------------------------- title card
      Card {
        id: titleCard
        anchors.fill: parent
        opacity: 0.93
        Column {
          anchors.centerIn: parent
          spacing: 18
          Text {
            anchors.horizontalCenter: parent.horizontalCenter
            text: "Which agent is waiting on me?"
            color: "white"
            font.family: shell.mono; font.pixelSize: 46; font.bold: true
          }
          Text {
            anchors.horizontalCenter: parent.horizontalCenter
            text: "Three coding agents. Three terminals. One of them needs you."
            color: "#a9b1d6"
            font.family: shell.mono; font.pixelSize: 20
          }
        }
      }

      // --------------------------------------------------------- end card
      Card {
        id: endCard
        anchors.fill: parent
        Column {
          anchors.centerIn: parent
          spacing: 14
          Text {
            anchors.horizontalCenter: parent.horizontalCenter
            text: String.fromCodePoint(0xF069A) // the widget's bar icon
            color: shell.fgColor
            font.family: shell.mono; font.pixelSize: 64
          }
          Text {
            anchors.horizontalCenter: parent.horizontalCenter
            text: "Agent Mission Control"
            color: "white"
            font.family: shell.mono; font.pixelSize: 48; font.bold: true
          }
          Text {
            anchors.horizontalCenter: parent.horizontalCenter
            text: "Every coding agent on your machine, one icon in your Omarchy bar."
            color: "#a9b1d6"
            font.family: shell.mono; font.pixelSize: 19
          }
          Text {
            anchors.horizontalCenter: parent.horizontalCenter
            text: "OpenCode + Claude Code · answer from the bar, the CLI or MCP"
            color: shell.dimColor
            font.family: shell.mono; font.pixelSize: 16
            bottomPadding: 14
          }
          Text {
            anchors.horizontalCenter: parent.horizontalCenter
            text: "github.com/rgolangh/agent-mission-control"
            color: shell.amber
            font.family: shell.mono; font.pixelSize: 22; font.bold: true
          }
        }
        Text {
          anchors.horizontalCenter: parent.horizontalCenter
          anchors.bottom: parent.bottom
          anchors.bottomMargin: 18
          text: "Scripted take: the daemon, bar widget and panel are real; the agents are a fake OpenCode server and replayed Claude Code hooks."
          color: "#414868"
          font.family: shell.mono; font.pixelSize: 11
        }
      }
    }

    // --------------------------------------------------------- recording
    property int frameNo: 0
    property double t0: 0
    property bool recording: false
    // One grab chain at a time. A grab of a window that just closed never
    // calls back; the clock then starts a new chain and the old one's
    // callbacks are ignored.
    property int chain: 0
    property double lastFrameAt: 0

    function grabFrame(c) {
      if (!recording || c !== chain) return
      var w = loader.item
      if (w && w.opened && w.panelCard.width > 0) {
        // Grab the panel bigger than it shows, so the zoom stays sharp.
        w.panelCard.grabToImage(function(r) {
          if (c !== win.chain) return
          panelImg.source = r.url
          saveFrame(c)
        }, Qt.size(Math.round(w.panelCard.width * 2.25), Math.round(w.panelCard.height * 2.25)))
      } else {
        saveFrame(c)
      }
    }
    function saveFrame(c) {
      var ms = Date.now() - t0
      frame.grabToImage(function(r) {
        if (c !== win.chain) return
        var n = ("000000" + win.frameNo).slice(-6)
        var t = ("0000000" + ms).slice(-7)
        r.saveToFile(shell.outDir + "/f" + n + "-" + t + ".png")
        win.frameNo++
        win.lastFrameAt = Date.now()
        Qt.callLater(win.grabFrame, c)
      })
    }

    // ---------------------------------------------------------- timeline
    property var steps: []
    property int stepIdx: 0

    Timer {
      id: clock
      interval: 20
      repeat: true
      onTriggered: {
        var now = Date.now() - win.t0
        if (Date.now() - win.lastFrameAt > 400) {
          win.chain++
          win.lastFrameAt = Date.now()
          win.grabFrame(win.chain)
        }
        while (win.stepIdx < win.steps.length && win.steps[win.stepIdx][0] <= now) {
          win.steps[win.stepIdx][1]()
          win.stepIdx++
        }
        if (win.stepIdx >= win.steps.length) {
          stop()
          win.recording = false
          console.log("frames:", win.frameNo)
          Qt.quit()
        }
      }
    }

    // Start once the widget sees all three agents busy.
    Timer {
      interval: 200
      repeat: true
      running: true
      property int tries: 0
      onTriggered: {
        tries++
        var w = loader.item
        if (w && w.api.runningCount === 3) {
          stop()
          shell.start()
        } else if (tries > 100) {
          console.log("TIMEOUT: the widget never saw three running workflows")
          Qt.quit()
        }
      }
    }
  }

  readonly property string outDir: Quickshell.env("PROMO_FRAMES")

  function unmarkup(s) { return s.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&") }

  function cue(name) { Quickshell.execDetached([Quickshell.env("PROMO_CUE"), name]) }

  function findChip(item, label) {
    if (!item) return null
    if (item.label === label && typeof item.activated === "function") return item
    var kids = item.children || []
    for (var i = 0; i < kids.length; i++) {
      var hit = findChip(kids[i], label)
      if (hit) return hit
    }
    return null
  }

  // Where a chip in the (grabbed) panel shows up in the scene.
  function chipPoint(label) {
    var w = loader.item
    var chip = findChip(w.panelCard, label)
    if (!chip) { console.log("no chip", label); return null }
    var p = chip.mapToItem(w.panelCard, chip.width / 2, chip.height * 0.6)
    return { x: panelImg.x + p.x, y: panelImg.y + p.y, chip: chip }
  }

  function iconPoint() {
    var p = loader.mapToItem(scene, 13, 15)
    return { x: p.x, y: p.y }
  }

  property var redis: null

  function start() {
    var w = loader.item
    var A = termAuth, S = termSync, C = termClaude
    var dim = dimColor, fg = fgColor
    A.lines = [["> Move session handling into the auth middleware.", green], ["", fg],
               ["→ Read middleware/auth.go", dim], ["→ Read internal/session/store.go", dim],
               ["→ Edit middleware/auth.go  (+42 −17)", dim]]
    S.lines = [["> TestSyncRetry fails about one run in five. Find out why and fix it.", green], ["", fg],
               ["→ Bash go test ./sync -run TestSyncRetry -count 50", dim], ["  FAIL  9 of 50 runs", "#f7768e"]]
    C.lines = [["> Add per-client rate limiting to the gateway", green], ["", fg],
               ["● Read(gateway/limit.go)", dim]]
    A.working = S.working = C.working = true

    function add(t, more) { t.lines = t.lines.concat(more) }

    win.steps = [
      [300, function() { cue("sync-0") }],
      [2600, function() { titleCard.opacity = 0 }],
      [3300, function() { caption.say("3 agents at work: OpenCode ×2, Claude Code ×1") }],
      [3900, function() { cue("claude-tool"); add(C, [["● Bash(go test ./gateway/...)", dim]]) }],
      [4700, function() { cue("sync-1"); add(S, [["→ Read sync/retry_test.go", dim], ["→ Read sync/clock.go", dim]]) }],
      [6000, function() {
        cue("ask"); A.working = false
        add(A, [["", fg], ["? Session store", amber], ["  Which session store should the middleware use?", fg],
                ["  1. Redis       shared across replicas", fg], ["  2. Postgres    reuse the main database", fg],
                ["  3. In-memory   single instance only", fg]])
        caption.say("")
      }],
      [6700, function() { scene.zoom = 1.5; caption.say("One needs you. The bar knows which.") }],
      [9500, function() { cursor.x = 1040; cursor.y = 300; cursor.opacity = 1 }],
      [9700, function() { cursor.moveTo(iconPoint()) }],
      [10500, function() { cursor.click(); w.open(); caption.say("Answer it from the bar.") }],
      [11700, function() { shell.redis = chipPoint("Redis"); if (shell.redis) cursor.moveTo(shell.redis) }],
      [12500, function() {
        cursor.click()
        if (shell.redis) { shell.redis.chip.selected = true; shell.redis.chip.activated() }
        A.working = true
        add(A, [["", fg], ["✓ Redis", green], ["→ Edit internal/session/redis.go", dim]])
      }],
      [13100, function() { cursor.x = cursor.x - 120; cursor.y = cursor.y + 160 }],
      [13300, function() { cue("sync-2"); add(S, [["→ Edit sync/retry.go  (+6 −3)", dim], ["→ Bash go test ./sync -run TestSyncRetry -count 200", dim]]) }],
      [13900, function() {
        cue("claude-done"); C.working = false
        add(C, [["● Added per-client rate limiting (429 + Retry-After). Tests pass.", fg]])
        caption.say("…and it gets back to work.")
      }],
      [15000, function() {
        cue("sync-done"); S.working = false
        add(S, [["  ok  200 of 200 runs", green], ["The retry test raced the fake clock; fixed.", fg]])
      }],
      [16700, function() {
        A.working = false
        add(A, [["→ Bash go test ./...", dim], ["Sessions now live in the auth middleware, on Redis. Tests pass.", fg]])
      }],
      [17700, function() { caption.say("All done. Back to calm."); cursor.moveTo(iconPoint()) }],
      [18500, function() { cursor.click(); w.close() }],
      [18900, function() { scene.zoom = 1; cursor.opacity = 0 }],
      [21000, function() { caption.say(""); endCard.opacity = 1 }],
      [27000, function() {}],
    ]
    win.t0 = Date.now()
    win.stepIdx = 0
    win.recording = true
    win.lastFrameAt = Date.now()
    clock.start()
    win.grabFrame(win.chain)
  }
}
