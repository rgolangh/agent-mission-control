
build:
	go build -o amc ./cmd/amc/

debug:
	# needs mutter-devkit installed
	G_MESSAGES_DEBUG="GNOME Shell" dbus-run-session gnome-shell --devkit --wayland

install: build
	./amc install --enable

.PHONY: build debug install
