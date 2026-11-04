package main

import (
	"reflect"
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
