package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// `git commits` and `git pulls` print author.login whether the API sends the Person object or a
// plain string (older servers).
func TestLegacyGitListsShowAuthorLoginFromPersonOrText(t *testing.T) {
	newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET " + gitBase() + "/commits": respond(200, `{"items":[{"sha":"0123456789abcdef","author":{"login":"alice","name":"Alice A","email":"a@x.io"},"message":"one"},{"sha":"fedcba9876543210","author":"bob","message":"two"}]}`),
		"GET " + gitBase() + "/pulls":   respond(200, `{"items":[{"number":1,"state":"open","author":{"login":"carol","name":"Carol"},"head":"feat","title":"t1"},{"number":2,"state":"open","author":"dave","head":"fix","title":"t2"}]}`),
	})
	var err error
	out := capture(t, func() { err = run(t, gitCommitsCmd, nil, nil) })
	if err != nil || !strings.Contains(out, "alice") || !strings.Contains(out, "bob") || strings.Contains(out, "map[") || strings.Contains(out, "Alice A") {
		t.Fatalf("commits: %v\n%s", err, out)
	}
	out = capture(t, func() { err = run(t, gitPullsCmd, nil, nil) })
	if err != nil || !strings.Contains(out, "carol") || !strings.Contains(out, "dave") || strings.Contains(out, "map[") {
		t.Fatalf("pulls: %v\n%s", err, out)
	}
}

func TestHelperIsConfiguredWithLeadingBang(t *testing.T) {
	if defaultHelperCmd != "!capsule git credential" {
		t.Fatalf("default helper %q", defaultHelperCmd)
	}
	probe := gitLoginProbe()
	if d := probe.Flags().Lookup("helper").DefValue; d != "!capsule git credential" {
		t.Fatalf("--helper default %q", d)
	}
	gc := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", gc)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := runGitSteps(helperSteps("https://git.example.com", defaultHelperCmd, false, true)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(gc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "helper = !capsule git credential") || !strings.Contains(string(b), "useHttpPath = true") {
		t.Fatalf("git config:\n%s", b)
	}
}

func TestCredentialSendsCLISessionIDForJWTAndLogoutRevokesIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/user/git-credentials": respond(200, `{"username":"alice","password":"cgt_x","expires_at":"2099-01-01T00:00:00Z","renew_after":"2098-01-01T00:00:00Z"}`),
		"DELETE /api/v1/user/git-tokens":    respond(200, `{"revoked":2}`),
	})
	old := cfg
	t.Cleanup(func() { cfg = old })

	cfg = &config.Config{APIURL: "http://x", Token: "eyJhbGciOi.jwt.sig"}
	if _, err := issueCredential("git.example.com", "acme/web"); err != nil {
		t.Fatal(err)
	}
	sid, _ := f.find("POST", "/api/v1/user/git-credentials").Body["cli_session_id"].(string)
	if len(sid) < 16 || sid != cfg.CLISessionID {
		t.Fatalf("cli_session_id %q (cfg %q)", sid, cfg.CLISessionID)
	}
	if again := cliSessionID(); again != sid {
		t.Fatal("the session id must be stable across calls")
	}

	var err error
	out := capture(t, func() { err = run(t, mustCmd(t, "logout"), nil, nil) })
	if err != nil || !strings.Contains(out, "2 short-lived") {
		t.Fatalf("logout: %v %q", err, out)
	}
	del := f.find("DELETE", "/api/v1/user/git-tokens")
	if del == nil || del.Query != "session_id="+sid {
		t.Fatalf("logout must revoke by session: %+v", del)
	}
	if cfg.CLISessionID != "" || cfg.Token != "" {
		t.Fatal("logout must forget the session")
	}

	// a cap_ API token identifies the session by itself: no cli_session_id
	cfg = &config.Config{APIURL: "http://x", Token: "cap_abc"}
	if cliSessionID() != "" {
		t.Fatal("cap_ tokens do not send a session id")
	}
}

func gitLoginProbe() *cobra.Command {
	c := &cobra.Command{Use: "probe"}
	addHelperFlags(c)
	return c
}
