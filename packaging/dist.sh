#!/bin/sh
# Build the release assets into dist/ (what .github/workflows/release.yml
# publishes, minus the .pkg.tar.zst and .rpm packages built from them):
#
#   amc-linux-{amd64,arm64}                  bare binaries (README's curl install)
#   amc-X.Y.Z-linux-{amd64,arm64}.tar.gz     binary + unit + GNOME extension + docs
#                                            (what agent-mission-control-bin repackages)
#   agent-mission-control-X.Y.Z.tar.gz       source (git archive of HEAD), for the
#                                            source PKGBUILD and the RPM spec
#   agent-mission-control@rgolangh.github.io.shell-extension.zip
#   SHA256SUMS
#
# Usage: packaging/dist.sh vX.Y.Z   (from a clean checkout of the tag)
set -eu
cd "$(dirname "$0")/.."
tag=${1:?usage: dist.sh vX.Y.Z}
v=${tag#v}
uuid=agent-mission-control@rgolangh.github.io
gnome="metadata.json extension.js client.js model.js stylesheet.css"

rm -rf dist
mkdir -p dist

for arch in amd64 arm64; do
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$tag" \
		-o dist/amc-linux-$arch ./cmd/amc

	d=dist/stage/amc-$v-linux-$arch
	mkdir -p "$d/gnome-shell/extensions/$uuid"
	cp dist/amc-linux-$arch "$d/amc"
	cp LICENSE README.md SECURITY.md packaging/systemd/agent-mission-control.service "$d/"
	for f in $gnome; do cp "ui/gnome/$f" "$d/gnome-shell/extensions/$uuid/"; done
	# Reproducible: fixed order, owner and mtime (the tagged commit's).
	tar --sort=name --owner=0 --group=0 --numeric-owner \
		--mtime="@$(git log -1 --format=%ct)" \
		-C dist/stage -czf "dist/amc-$v-linux-$arch.tar.gz" "amc-$v-linux-$arch"
done
rm -rf dist/stage

git archive --format=tar.gz --prefix="agent-mission-control-$v/" \
	-o "dist/agent-mission-control-$v.tar.gz" HEAD

(cd ui/gnome && zip -qX "../../dist/$uuid.shell-extension.zip" $gnome)

(cd dist && sha256sum -- * > SHA256SUMS)
cat dist/SHA256SUMS
