package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispatchNoArgsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := dispatch(nil, &stdout, &stderr)

	if rc != 0 {
		t.Fatalf("expected rc=0 for no args, got %d (stderr=%q)", rc, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("expected usage on stdout, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestDispatchHelpFlagsPrintUsage(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := dispatch([]string{arg}, &stdout, &stderr)

			if rc != 0 {
				t.Fatalf("expected rc=0 for %q, got %d", arg, rc)
			}
			if !strings.Contains(stdout.String(), "Usage:") {
				t.Fatalf("expected usage on stdout for %q, got %q", arg, stdout.String())
			}
		})
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := dispatch([]string{"definitely-not-a-command"}, &stdout, &stderr)

	if rc != 2 {
		t.Fatalf("expected rc=2 for unknown command, got %d", rc)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("expected 'unknown command' on stderr, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("expected usage on stderr after error, got %q", stderr.String())
	}
}

func TestDispatchAllCommandsRegistered(t *testing.T) {
	expected := []string{
		"init", "capture", "task-capture", "task-tool-capture",
		"check-feedback", "flush", "companion", "status",
		"log", "reset", "config", "install", "uninstall",
	}
	got := make(map[string]bool, len(commands))
	for _, c := range commands {
		got[c.name] = true
	}
	for _, name := range expected {
		if !got[name] {
			t.Errorf("subcommand %q not registered", name)
		}
	}
	if len(commands) != len(expected) {
		t.Errorf("expected %d commands, got %d", len(expected), len(commands))
	}
}
