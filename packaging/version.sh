#!/bin/sh
# The release version lives in the files that ship it without going through
# the Go linker: the Omarchy plugin manifest, the GNOME extension metadata
# and the RPM spec. (The binary gets it from -ldflags at build time.)
#
#   packaging/version.sh set 0.2.0     bump all of them, and the docs' pacman -U URLs (commit, then tag v0.2.0)
#   packaging/version.sh check v0.2.0  fail unless all of them say 0.2.0 (release CI)
#   packaging/version.sh show          print what each one says
set -eu
cd "$(dirname "$0")/.."

manifest=ui/omarchy/manifest.json
metadata=ui/gnome/metadata.json
spec=packaging/rpm/agent-mission-control.spec

show() {
	sed -n 's/^  "version": "\(.*\)",$/\1/p' "$manifest"
	sed -n 's/^  "version-name": "\(.*\)"$/\1/p' "$metadata"
	sed -n 's/^Version:[[:space:]]*\(.*\)$/\1/p' "$spec"
}

case "${1:-}" in
set)
	v=${2:?usage: version.sh set X.Y.Z}
	v=${v#v}
	echo "$v" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo "not X.Y.Z: $v" >&2; exit 1; }
	sed -i "s/^  \"version\": \".*\",\$/  \"version\": \"$v\",/" "$manifest"
	sed -i "s/^  \"version-name\": \".*\"\$/  \"version-name\": \"$v\"/" "$metadata"
	# The release download URL and file name in the docs (until the AUR package exists).
	sed -i -E "s#/download/v[0-9.]+/agent-mission-control-bin-[0-9.]+-1-#/download/v$v/agent-mission-control-bin-$v-1-#g; s#\./agent-mission-control-bin-[0-9.]+-1-#./agent-mission-control-bin-$v-1-#g" \
		README.md docs/demo/promo.md
	old=$(sed -n 's/^Version:[[:space:]]*\(.*\)$/\1/p' "$spec")
	if [ "$old" != "$v" ]; then
		sed -i "s/^Version:\([[:space:]]*\).*\$/Version:\1$v/; s/^Release:\([[:space:]]*\).*\$/Release:\11%{?dist}/" "$spec"
		entry="* $(LC_ALL=C date +'%a %b %d %Y') Roy Golan <royg21@gmail.com> - $v-1\n- Release v$v\n"
		sed -i "s/^%changelog\$/%changelog\n$entry/" "$spec"
	fi
	show
	;;
check)
	v=${2:?usage: version.sh check vX.Y.Z}
	v=${v#v}
	bad=0
	for got in $(show); do
		[ "$got" = "$v" ] || bad=1
	done
	[ "$(show | wc -l)" -eq 3 ] || bad=1
	if [ $bad -ne 0 ]; then
		echo "version mismatch: tag says $v, files say:" >&2
		show >&2
		echo "run: packaging/version.sh set $v, commit, and re-tag" >&2
		exit 1
	fi
	;;
show) show ;;
*)
	sed -n '2,9s/^# \{0,1\}//p' "$0" >&2
	exit 2
	;;
esac
