//go:build !js

package main

import (
	"os"
	"os/exec"
	"strings"
)

// runGitSteps runs git directly (native build).
func runGitSteps(steps []gitStep) error {
	for _, s := range steps {
		c := exec.Command("git", s.Args...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil && !s.IgnoreError {
			return codedErr(exitGeneric, "git %s failed: %v", strings.Join(redactArgs(s.Args), " "), err)
		}
	}
	return nil
}

// gitOutput runs git and returns its stdout (native build only).
func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

const canReadGit = true
