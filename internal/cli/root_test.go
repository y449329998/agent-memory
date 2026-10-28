package cli

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRun_UnknownCommandExit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"nope"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout must be empty, got %q", stdout.String())
	}
	for _, want := range []string{"unknown command", "usage:"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), want)
		}
	}
}

func TestRun_VersionPrintsVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit code = %d, want 0", args, code)
		}
		if stderr.Len() != 0 {
			t.Fatalf("%v: stderr must be empty, got %q", args, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "agent-memory ") {
			t.Fatalf("%v: stdout = %q, want prefix %q", args, stdout.String(), "agent-memory ")
		}
	}
}

func TestRun_NoArgsPrintsUsageToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout must be empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), "usage:")
	}
}

func TestRun_DispatchesRegisteredCommand(t *testing.T) {
	const name = "stub"
	var gotArgs []string
	register(command{
		name: name,
		desc: "test stub",
		run: func(args []string, stdout, _ io.Writer) int {
			gotArgs = args
			fmt.Fprintf(stdout, "stub:%d\n", len(args))
			return 1
		},
	})
	t.Cleanup(func() { delete(commands, name) })

	var stdout, stderr bytes.Buffer
	code := Run([]string{name, "a", "b"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1: subcommand return code must pass through", code)
	}
	if want := []string{"a", "b"}; !slices.Equal(gotArgs, want) {
		t.Fatalf("args = %q, want %q", gotArgs, want)
	}
	if got, want := stdout.String(), "stub:2\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr must be empty, got %q", stderr.String())
	}
}

func TestRegister_RejectsDuplicatesAndReservedNames(t *testing.T) {
	const name = "tmp-register"
	register(command{name: name, desc: "temporary command"})
	t.Cleanup(func() { delete(commands, name) })

	cases := []struct {
		desc    string
		cmd     command
		wantMsg string
	}{
		{"duplicate", command{name: name}, fmt.Sprintf("cli: duplicate command %q", name)},
		{"reserved", command{name: "help"}, `cli: command name is reserved: "help"`},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("register(%q) did not panic", tc.cmd.name)
				}
				if got := fmt.Sprint(r); got != tc.wantMsg {
					t.Fatalf("panic = %q, want %q", got, tc.wantMsg)
				}
			}()
			register(tc.cmd)
		})
	}

	// 上述 panic 不得改动全局命令表。
	if _, ok := commands[name]; !ok {
		t.Fatalf("register panic dropped existing command %q", name)
	}
	if _, ok := commands["help"]; ok {
		t.Fatalf("register panic inserted reserved command %q", "help")
	}
}
