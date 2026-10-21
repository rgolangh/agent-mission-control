#!/bin/sh
# Build agent-mission-control-bin packages for x86_64 and aarch64 from the
# release tarballs in DIST (from packaging/dist.sh), without downloading:
# makepkg uses a source file already present in the build directory. The
# package only repackages a prebuilt static binary (options=(!strip !debug)),
# so aarch64 is built on x86_64 by overriding CARCH.
#
#   packaging/arch/build.sh vX.Y.Z DIST OUTDIR      (run as a non-root user)
#
# The PKGBUILD must already point at vX.Y.Z (packaging/arch/update.sh).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
tag=${1:?usage: build.sh vX.Y.Z DIST OUTDIR}
dist=$(cd "${2:?}" && pwd)
mkdir -p "${3:?}"
out=$(cd "$3" && pwd)
v=${tag#v}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$here/agent-mission-control-bin/PKGBUILD" "$here/agent-mission-control-bin/agent-mission-control.install" "$work/"
cp "$dist/amc-$v-linux-amd64.tar.gz" "$dist/amc-$v-linux-arm64.tar.gz" "$work/"

for carch in x86_64 aarch64; do
	conf=$work/makepkg-$carch.conf
	sed -e "s/^CARCH=.*/CARCH=\"$carch\"/" -e "s/^CHOST=.*/CHOST=\"$carch-pc-linux-gnu\"/" \
		/etc/makepkg.conf > "$conf"
	(cd "$work" && PKGDEST="$out" SRCDEST="$work" BUILDDIR="$work/build-$carch" \
		makepkg --config "$conf" --clean --cleanbuild --force --noconfirm)
done
ls -l "$out"
