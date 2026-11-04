package main

import (
	"reflect"
	"strings"
	"testing"
)

// Scripts and docs use both spellings, so an alias must keep dispatching to
// the same handler as its main name.
func TestCommandAliases(t *testing.T) {
	for alias, name := range map[string]string{"status": "list", "show": "get", "--version": "version"} {
		a, b := commands[alias], commands[name]
		if a == nil || b == nil || reflect.ValueOf(a).Pointer() != reflect.ValueOf(b).Pointer() {
			t.Errorf("%s is not an alias of %s", alias, name)
		}
	}
}

// usage() and the commands map are kept by hand; every command usage lists
// must dispatch.
func TestUsageCommandsDispatch(t *testing.T) {
	for _, line := range strings.Split(usageText, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == "amc" && commands[f[1]] == nil {
			t.Errorf("usage lists %q, which does not dispatch", f[1])
		}
	}
}
