package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/HarnageaGabriel/trasloco/internal/cli"
	"github.com/HarnageaGabriel/trasloco/internal/redact"
)

func TestGuardedPanic(t *testing.T) {
	var dst bytes.Buffer
	r := redact.New()
	r.Add("panic-secret")
	code := guarded(&dst, r, func() int { panic("panic-secret") })
	if code != cli.ExitError || strings.Contains(dst.String(), "panic-secret") || !strings.Contains(dst.String(), redact.Replacement()) {
		t.Fatal("panic boundary failed")
	}
	if guarded(&dst, r, func() int { return cli.ExitOK }) != cli.ExitOK {
		t.Fatal("success changed")
	}
}

func TestRunVersion(t *testing.T) {
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"trasloco", "version"}
	if code := run(); code != cli.ExitOK {
		t.Fatalf("run exit %d", code)
	}
}
