package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_UnknownCommandExit2(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"nope"}, &out, &out)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Fatalf("want usage text, got %q", out.String())
	}
}

func TestRun_VersionPrintsVersion(t *testing.T) {
	var out bytes.Buffer
	if code := Run([]string{"version"}, &out, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "agent-memory ") {
		t.Fatalf("got %q", out.String())
	}
}
