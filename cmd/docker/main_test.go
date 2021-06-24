package main

import "testing"

func TestSplitCommand(t *testing.T) {
	args := []string{
		"container", "ls", "-a",
		"--",
		"container", "stop", "my-container",
	}
	commands := splitCommands(args)
	if len(commands) != 2 {
		t.Error("expected two commands")
	}
	if !arrCmp(commands[0], []string{"container", "ls", "-a"}) {
		t.Error("parsed wrong command")
	}
	if !arrCmp(commands[1], []string{"container", "stop", "my-container"}) {
		t.Error("parsed wrong command")
	}
}

func arrCmp(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, aa := range a {
		bb := b[i]
		if aa != bb {
			return false
		}
	}
	return true
}
