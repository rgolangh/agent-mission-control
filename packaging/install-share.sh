#!/bin/sh
# Install the per-user integrations' files where a package ships them, so
# `amc install` (packaged mode) links them into each home instead of copying
# them, and package upgrades update them:
#
#   DEST/opencode/agent-mission-control-bridge.js
#   DEST/claude-code/...                           (the Claude Code hooks plugin)
#   DEST/omarchy/{manifest.json,Widget.qml,Api.qml,README.md}
#
# DEST is $pkgdir/usr/share/agent-mission-control (install.SystemShare), or a
# staging directory. The file lists match what the binary embeds (assets.go).
#
# Usage: packaging/install-share.sh DEST    (from the repository root)
set -eu
cd "$(dirname "$0")/.."
dest=${1:?usage: install-share.sh DEST}

install -Dm644 integrations/opencode/agent-mission-control-bridge.js "$dest/opencode/agent-mission-control-bridge.js"
(cd integrations/claude-code && find . -type f) | while read -r f; do
	install -Dm644 "integrations/claude-code/$f" "$dest/claude-code/$f"
done
install -Dm644 -t "$dest/omarchy" ui/omarchy/manifest.json ui/omarchy/Widget.qml ui/omarchy/Api.qml ui/omarchy/README.md
