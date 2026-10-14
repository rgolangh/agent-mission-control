// Package missioncontrol holds the files the `mission-control install`
// command writes out: the agent integrations and desktop UIs, embedded in the
// binary so they always match the installed version.
package missioncontrol

import "embed"

// Assets are paths relative to the repository root, e.g.
// "integrations/opencode/mission-control-bridge.js". The `all:` prefix keeps
// dot-directories such as integrations/claude-code/.claude-plugin.
//
//go:embed all:integrations
//go:embed ui/omarchy/manifest.json ui/omarchy/Widget.qml ui/omarchy/Api.qml ui/omarchy/README.md
var Assets embed.FS
