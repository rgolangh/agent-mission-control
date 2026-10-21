
build:
	go build -o amc ./cmd/amc/

debug:
	# needs mutter-devkit installed
	G_MESSAGES_DEBUG="GNOME Shell" dbus-run-session gnome-shell --devkit --wayland

install: build
	./amc install --enable

# make release V=0.2.0: bump the versioned files, commit, tag and push.
# The tag push runs .github/workflows/release.yml, which only checks them.
release:
	@test -n "$(V)" || { echo "usage: make release V=X.Y.Z" >&2; exit 1; }
	@test "$$(git branch --show-current)" = main || { echo "not on main" >&2; exit 1; }
	@test -z "$$(git status --porcelain --untracked-files=no)" || { echo "uncommitted changes" >&2; exit 1; }
	git pull --ff-only
	packaging/version.sh set $(V)
	git commit -am "Release v$(V:v%=%)"
	git tag v$(V:v%=%)
	git push --atomic origin main v$(V:v%=%)

.PHONY: build debug install release
