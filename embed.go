// Package agentmissioncontrol holds the files the `amc install`
// command writes out: the agent integrations and desktop UIs, embedded in the
// binary so they always match the installed version.
package agentmissioncontrol

import "embed"

// Assets are paths relative to the repository root, e.g.
// "integrations/opencode/agent-mission-control-bridge.js". The `all:` prefix keeps
// dot-directories such as integrations/claude-code/.claude-plugin.
//
//go:embed all:integrations
//go:embed ui/omarchy/manifest.json ui/omarchy/Widget.qml ui/omarchy/Api.qml ui/omarchy/README.md
//go:embed ui/gnome/metadata.json ui/gnome/extension.js ui/gnome/client.js ui/gnome/model.js ui/gnome/stylesheet.css
var Assets embed.FS
