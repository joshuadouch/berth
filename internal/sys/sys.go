// Package sys runs system commands for the engine.
package sys

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
)

// Runner runs commands. With DryRun set it logs them and does nothing.
type Runner struct {
	DryRun bool
	Log    *slog.Logger
	// Env is added to every command's environment.
	Env []string
}

// Cmd describes one command.
type Cmd struct {
	Name  string
	Args  []string
	Stdin io.Reader
	// Stdout, when set, receives standard output instead of the result.
	Stdout io.Writer
	Env    []string
	Dir    string
}

// Error is a failed command with its output.
type Error struct {
	Cmd    string
	Err    error
	Output string
}

func (e *Error) Error() string {
	out := strings.TrimSpace(e.Output)
	if len(out) > 4000 {
		out = "…" + out[len(out)-4000:]
	}
	if out == "" {
		return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
	}
	return fmt.Sprintf("%s: %v\n%s", e.Cmd, e.Err, out)
}

// Exec runs c and returns its combined output (or stderr when Stdout is set).
func (r *Runner) Exec(ctx context.Context, c Cmd) (string, error) {
	desc := c.Name + " " + strings.Join(c.Args, " ")
	if r.DryRun {
		if r.Log != nil {
			r.Log.Info("dry run", "cmd", desc)
		}
		return "", nil
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Stdin = c.Stdin
	cmd.Dir = c.Dir
	if c.Env != nil {
		cmd.Env = append(append([]string{}, c.Env...), r.Env...)
	}
	var out bytes.Buffer
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	} else {
		cmd.Stdout = &out
	}
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return out.String(), &Error{Cmd: shortName(c.Name), Err: err, Output: out.String()}
	}
	return out.String(), nil
}

// Run runs a command with arguments.
func (r *Runner) Run(ctx context.Context, name string, args ...string) (string, error) {
	return r.Exec(ctx, Cmd{Name: name, Args: args})
}

// Shell runs a command line with sh -c.
func (r *Runner) Shell(ctx context.Context, line string) (string, error) {
	return r.Exec(ctx, Cmd{Name: "/bin/sh", Args: []string{"-c", line}})
}

func shortName(n string) string {
	if i := strings.LastIndex(n, "/"); i >= 0 {
		return n[i+1:]
	}
	return n
}
