package main

import (
	"io"
	"os"
	"strings"
)

// gitStep is one `git` invocation. Steps run in order and stop at the first
// failure unless IgnoreError is set (for example unsetting a key that may not
// exist yet).
type gitStep struct {
	Args        []string
	IgnoreError bool
}

func readAllStdin() (string, error) {
	b, err := io.ReadAll(os.Stdin)
	return string(b), err
}

// redactArgs hides credentials embedded in URLs from error messages.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = redactURL(a)
	}
	return out
}

// redactURL removes user:password@ from a URL-looking string.
func redactURL(s string) string {
	i := strings.Index(s, "://")
	if i < 0 {
		return s
	}
	rest := s[i+3:]
	at := strings.Index(rest, "@")
	slash := strings.Index(rest, "/")
	if at >= 0 && (slash < 0 || at < slash) {
		return s[:i+3] + "***@" + rest[at+1:]
	}
	return s
}
