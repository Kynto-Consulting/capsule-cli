//go:build js

package main

import (
	"encoding/json"
	"os"
	"strings"
)

// Under WASM there is no os/exec: the Node wrapper runs git after this process
// exits, from the queue file named by CAPSULE_POST_ACTIONS (the same queue
// `ci init --set-secrets` uses for gh). The wrapper only runs `git config
// --global credential.*`, `git config --local credential.*`, `git clone` and
// `git fetch`/`git checkout` and refuses anything else.
func runGitSteps(steps []gitStep) error {
	path := os.Getenv("CAPSULE_POST_ACTIONS")
	if path == "" {
		return codedErr(exitGeneric, "this command needs to run git, but the installed npm wrapper does not support it; update @kyntocg/capsule-cli to 0.7.0 or newer")
	}
	var list []map[string]any
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &list)
	}
	for _, s := range steps {
		list = append(list, map[string]any{"cmd": "git", "args": s.Args, "ignoreError": s.IgnoreError})
	}
	b, _ := json.Marshal(list)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return codedErr(exitGeneric, "queueing git commands: %v", err)
	}
	return nil
}

// gitOutput is unavailable under WASM.
func gitOutput(args ...string) (string, error) {
	return "", codedErr(exitGeneric, "reading git state is not available in the WASM build")
}

const canReadGit = false

var _ = strings.TrimSpace
