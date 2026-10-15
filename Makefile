
build:
	go build -o mission-control ./cmd/mission-control/

debug:
	# needs mutter-devkit installed
	G_MESSAGES_DEBUG="GNOME Shell" dbus-run-session gnome-shell --devkit --wayland

install: build
	./mission-control install --enable

.PHONY: build debug install

