package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

// Exit codes (docs/git-capsule/CLI.md section 2). `capsule help exit-codes`
// prints the same table.
const (
	exitOK       = 0
	exitGeneric  = 1
	exitUsage    = 2
	exitAuth     = 3
	exitDenied   = 4
	exitBlocked  = 5
	exitTimeout  = 6
	exitGit      = 7
	exitApproval = 8
	exitConflict = 9
)

// exitError carries an explicit exit code. Unwrap keeps the original error
// (for example a *client.APIError) reachable with errors.As.
type exitError struct {
	code int
	msg  string
	err  error
}

func (e *exitError) Error() string { return e.msg }
func (e *exitError) Unwrap() error { return e.err }

func usageErr(format string, a ...any) error {
	return &exitError{code: exitUsage, msg: fmt.Sprintf(format, a...)}
}

func codedErr(code int, format string, a ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, a...)}
}

// classifyAPI maps a server error to an exit code. The machine code wins over
// the HTTP status because a rule block can arrive as 403, 409 or 422.
func classifyAPI(ae *client.APIError) int {
	switch ae.Code {
	case "PROTECTED_BRANCH", "CHECKS_REQUIRED", "REVIEWS_REQUIRED", "POLICY_BLOCKED",
		"CODEOWNER_REQUIRED", "SELF_APPROVAL_FORBIDDEN", "SIGNATURE_REQUIRED",
		"QUOTA_EXCEEDED", "MODEL_UNAVAILABLE", "NO_MODEL", "MODEL_ACCESS": // AI: budget spent, or the model cannot be used
		return exitBlocked
	case "GIT_NOT_READY", "UPSTREAM_GIT_ERROR":
		return exitGit
	case "APPROVAL_REQUIRED":
		return exitApproval
	case "IDEMPOTENCY_CONFLICT":
		return exitConflict
	}
	switch ae.Status {
	case 401:
		return exitAuth
	case 403, 404:
		return exitDenied
	case 409:
		return exitConflict
	case 400, 422:
		return exitUsage
	case 501, 502, 503, 504:
		return exitGit
	}
	return exitGeneric
}

// exitCodeFor is the single place that turns any error into a process exit code.
func exitCodeFor(err error) int {
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if ae, ok := client.AsAPIError(err); ok {
		return classifyAPI(ae)
	}
	m := err.Error()
	for _, s := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "required flag(s)",
		"accepts ", "requires at least", "requires at most", "invalid argument", "flag needs an argument",
		"--org is required", "--project is required"} {
		if strings.Contains(m, s) {
			return exitUsage
		}
	}
	return exitGeneric
}

// gitErr turns a server error into a message with the machine code and a hint
// for the cases where the server may simply not offer the function yet. what
// names the feature ("pull requests", "security").
func gitErr(err error, what string) error {
	if err == nil {
		return nil
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return err
	}
	ae, ok := client.AsAPIError(err)
	if !ok {
		return err
	}
	code := ae.Code
	if code == "" {
		code = fmt.Sprintf("HTTP_%d", ae.Status)
	}
	msg := fmt.Sprintf("error %s: %s", code, ae.Message)
	switch ae.Status {
	case 401:
		msg += "\n  Not signed in or the token is invalid: run `capsule login` (or set CAPSULE_TOKEN)."
	case 404:
		if ae.Code == "" || ae.Code == "NOT_FOUND" {
			msg += fmt.Sprintf("\n  Not found: the resource does not exist, you cannot see it, or this server does not support %s yet.", what)
		}
	case 501:
		msg += fmt.Sprintf("\n  %s is not enabled on this Capsule server (501); ask the platform administrator.", what)
	case 429:
		msg += "\n  Rate limited: retry in a moment."
	}
	if ae.RequestID != "" {
		msg += fmt.Sprintf("\n  request_id: %s", ae.RequestID)
	}
	return &exitError{code: classifyAPI(ae), msg: msg, err: err}
}

const exitCodesHelp = `Exit codes (stable; safe to branch on in scripts and agents):

  0  OK
  1  Unclassified error
  2  Incorrect usage (flags, arguments, no linked project, missing --yes)
  3  Not authenticated / invalid token
  4  Not allowed or not found (the server hides existence)
  5  Blocked by a rule: PROTECTED_BRANCH, CHECKS_REQUIRED, REVIEWS_REQUIRED,
     POLICY_BLOCKED, CODEOWNER_REQUIRED; AI: QUOTA_EXCEEDED (daily budget spent),
     MODEL_UNAVAILABLE / NO_MODEL / MODEL_ACCESS (the model cannot be used: see capsule ai models)
  6  Wait timed out (--wait) or the run ended in failure with --exit-status
  7  Git not ready or engine error: GIT_NOT_READY, UPSTREAM_GIT_ERROR (and 501/5xx)
  8  Pending approval: APPROVAL_REQUIRED
  9  Concurrency conflict (409: head changed, idempotency key reused)
`

var exitCodesCmd = &cobra.Command{
	Use:   "exit-codes",
	Short: "Exit codes returned by capsule",
	Long:  exitCodesHelp,
}

func init() {
	rootCmd.AddCommand(exitCodesCmd)
	rootCmd.SilenceErrors = true // main prints the error once, with the classified exit code
	rootCmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &exitError{code: exitUsage, msg: err.Error(), err: err}
	})
}

// exitWith prints err (if any) to stderr and exits with its code.
func exitWith(err error) {
	if err != nil && err.Error() != "" {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(exitCodeFor(err))
}
