#!/bin/sh
# Point both PKGBUILDs at a release: set pkgver, reset pkgrel to 1, fill in
# the sha256 sums from that release's SHA256SUMS, and regenerate .SRCINFO.
#
#   packaging/arch/update.sh vX.Y.Z dist/SHA256SUMS         (CI, or after dist.sh)
#   gh release download vX.Y.Z -p SHA256SUMS -D /tmp/r && \
#     packaging/arch/update.sh vX.Y.Z /tmp/r/SHA256SUMS     (before an AUR push)
#
# Needs makepkg (for .SRCINFO).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
tag=${1:?usage: update.sh vX.Y.Z path/to/SHA256SUMS}
sums=${2:?usage: update.sh vX.Y.Z path/to/SHA256SUMS}
v=${tag#v}

sumof() {
	s=$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$sums")
	[ -n "$s" ] || { echo "update.sh: $1 not in $sums" >&2; exit 1; }
	echo "$s"
}

set_pkg() { # dir, then sed expressions
	d=$here/$1
	shift
	sed -i -e "s/^pkgver=.*/pkgver=$v/" -e "s/^pkgrel=.*/pkgrel=1/" "$@" "$d/PKGBUILD"
	(cd "$d" && makepkg --printsrcinfo > .SRCINFO)
}

set_pkg agent-mission-control-bin \
	-e "s/^sha256sums_x86_64=.*/sha256sums_x86_64=('$(sumof "amc-$v-linux-amd64.tar.gz")')/" \
	-e "s/^sha256sums_aarch64=.*/sha256sums_aarch64=('$(sumof "amc-$v-linux-arm64.tar.gz")')/"
set_pkg agent-mission-control \
	-e "s/^sha256sums=.*/sha256sums=('$(sumof "agent-mission-control-$v.tar.gz")')/"
echo "updated PKGBUILDs and .SRCINFO to $v"
