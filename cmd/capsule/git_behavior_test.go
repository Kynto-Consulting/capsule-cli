package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/client"
	"github.com/kynto-consulting/capsule/cli/internal/config"
)

type fakeReq struct {
	Method, Path, Query string
	Body                map[string]any
	Header              http.Header
}

// fakeAPI answers by "METHOD /path" (no query) and records requests.
type fakeAPI struct {
	mu   sync.Mutex
	reqs []fakeReq
	h    map[string]func(w http.ResponseWriter, r *http.Request)
}

func respond(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func apiErr(status int, code, msg string) func(http.ResponseWriter, *http.Request) {
	return respond(status, `{"error":{"code":"`+code+`","message":"`+msg+`"}}`)
}

func newFake(t *testing.T, h map[string]func(http.ResponseWriter, *http.Request)) *fakeAPI {
	t.Helper()
	f := &fakeAPI{h: h}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		f.reqs = append(f.reqs, fakeReq{r.Method, r.URL.Path, r.URL.RawQuery, body, r.Header.Clone()})
		fn := f.h[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if fn == nil {
			respond(404, `{"error":{"code":"NOT_FOUND","message":"`+r.Method+" "+r.URL.Path+`"}}`)(w, r)
			return
		}
		fn(w, r)
	}))
	t.Cleanup(srv.Close)
	old, oldCfg := apiClient, cfg
	apiClient = client.New(srv.URL, "tok")
	t.Cleanup(func() { apiClient, cfg = old, oldCfg })
	t.Setenv("CAPSULE_ORG_ID", tOrg)
	t.Setenv("CAPSULE_PROJECT_ID", tProj)
	t.Setenv("CAPSULE_TOKEN", "")
	t.Setenv("CI", "true")
	return f
}

func (f *fakeAPI) count(method, pathSuffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Method == method && strings.HasSuffix(r.Path, pathSuffix) {
			n++
		}
	}
	return n
}

func (f *fakeAPI) find(method, pathSuffix string) *fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.reqs {
		if f.reqs[i].Method == method && strings.HasSuffix(f.reqs[i].Path, pathSuffix) {
			return &f.reqs[i]
		}
	}
	return nil
}

func gitBase() string { return "/api/v1/orgs/" + tOrg + "/projects/" + tProj + "/git" }
func projURL() string { return "/api/v1/orgs/" + tOrg + "/projects/" + tProj }

func mustCmd(t *testing.T, words string) *cobra.Command {
	t.Helper()
	c, rest, err := rootCmd.Find(strings.Fields(words))
	if err != nil || c == rootCmd || len(rest) > 0 {
		t.Fatalf("no command %q", words)
	}
	return c
}

func TestClassifyAPIExitCodes(t *testing.T) {
	cases := []struct {
		status int
		code   string
		want   int
	}{
		{401, "UNAUTHORIZED", exitAuth},
		{403, "FORBIDDEN", exitDenied},
		{404, "NOT_FOUND", exitDenied},
		{403, "PROTECTED_BRANCH", exitBlocked},
		{409, "CHECKS_REQUIRED", exitBlocked},
		{422, "REVIEWS_REQUIRED", exitBlocked},
		{422, "POLICY_BLOCKED", exitBlocked},
		{403, "CODEOWNER_REQUIRED", exitBlocked},
		{403, "SELF_APPROVAL_FORBIDDEN", exitBlocked},
		{503, "GIT_NOT_READY", exitGit},
		{502, "UPSTREAM_GIT_ERROR", exitGit},
		{501, "", exitGit},
		{409, "APPROVAL_REQUIRED", exitApproval},
		{409, "IDEMPOTENCY_CONFLICT", exitConflict},
		{409, "HEAD_CHANGED", exitConflict},
		{422, "VALIDATION", exitUsage},
		{429, "RATE_LIMITED", exitGeneric},
		{500, "INTERNAL", exitGeneric},
	}
	for _, c := range cases {
		if got := classifyAPI(&client.APIError{Status: c.status, Code: c.code}); got != c.want {
			t.Errorf("%d %s -> %d, want %d", c.status, c.code, got, c.want)
		}
	}
}

func TestExitCodesFromCommands(t *testing.T) {
	merge := gitBase() + "/pulls/12/merge"
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST " + merge:                          apiErr(422, "POLICY_BLOCKED", "1 new high alert in src/db.ts:41"),
		"GET " + gitBase() + "/pulls/13":         apiErr(401, "UNAUTHORIZED", "bad token"),
		"GET " + gitBase() + "/pulls/14":         apiErr(404, "NOT_FOUND", "no such pull request"),
		"POST " + gitBase() + "/pulls/15/merge":  apiErr(409, "IDEMPOTENCY_CONFLICT", "key reused"),
		"POST " + gitBase() + "/pulls/16/merge":  apiErr(409, "APPROVAL_REQUIRED", "needs approval"),
		"GET " + gitBase() + "/pulls/17":         apiErr(503, "GIT_NOT_READY", "engine starting"),
		"DELETE " + gitBase() + "/branches/main": apiErr(403, "PROTECTED_BRANCH", "the branch 'main' is protected by the rule 'main'"),
	})
	_ = f
	check := func(words string, args []string, flags map[string][]string, want int, contains string) {
		t.Helper()
		var err error
		capture(t, func() { err = run(t, mustCmd(t, words), args, flags) })
		if got := exitCodeFor(err); got != want {
			t.Errorf("%s %v: exit %d, want %d (err: %v)", words, args, got, want, err)
		}
		if contains != "" && (err == nil || !strings.Contains(err.Error(), contains)) {
			t.Errorf("%s %v: error %v should contain %q", words, args, err, contains)
		}
	}
	check("pr merge", []string{"12"}, f2("squash", "true"), exitBlocked, "error POLICY_BLOCKED: 1 new high alert")
	check("pr view", []string{"13"}, nil, exitAuth, "capsule login")
	check("pr view", []string{"14"}, nil, exitDenied, "NOT_FOUND")
	check("pr merge", []string{"15"}, nil, exitConflict, "IDEMPOTENCY_CONFLICT")
	check("pr merge", []string{"16"}, nil, exitApproval, "APPROVAL_REQUIRED")
	check("pr view", []string{"17"}, nil, exitGit, "GIT_NOT_READY")
	check("branch delete", []string{"main"}, f2("yes", "true"), exitBlocked, "PROTECTED_BRANCH")
	// usage errors never reach the server
	n := len(f.reqs)
	check("pr view", []string{"abc"}, nil, exitUsage, "must be a number")
	check("pr merge", []string{"12"}, f2("squash", "true", "rebase", "true"), exitUsage, "only one of")
	check("pr review", []string{"12"}, nil, exitUsage, "exactly one")
	check("security alerts dismiss", []string{uuidA}, f2("reason", "wont_fix", "expires", "90d"), exitUsage, "--why is required")
	check("security alerts dismiss", []string{uuidA}, f2("reason", "wont_fix", "why", "long enough reason"), exitUsage, "--expires is required")
	check("security alerts dismiss", []string{uuidA}, f2("reason", "bogus", "why", "long enough reason", "expires", "1d"), exitUsage, "--reason")
	check("git tokens create", nil, f2("name", "x", "expires", "400d"), exitUsage, "between 1d and 90d")
	if len(f.reqs) != n {
		t.Errorf("usage errors must not call the API (%d extra requests)", len(f.reqs)-n)
	}
}

// runOutM is runOut for flags inherited from the root command (--jq,
// --no-header): cobra merges them into cmd.Flags() when it parses real arguments.
func runOutM(t *testing.T, c *cobra.Command, args []string, flags map[string][]string) (string, error) {
	t.Helper()
	_ = c.ParseFlags(nil)
	return runOut(t, c, args, flags)
}

func f2(kv ...string) map[string][]string { return f(kv...) }

func TestConfirmationsNeedYes(t *testing.T) {
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"DELETE " + gitBase() + "/branches/fix/x": respond(204, ""),
	})
	var err error
	capture(t, func() { err = run(t, mustCmd(t, "branch delete"), []string{"fix/x"}, nil) })
	if exitCodeFor(err) != exitUsage || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without --yes: %v", err)
	}
	if f.count("DELETE", "fix%2Fx") != 0 && f.count("DELETE", "fix/x") != 0 {
		t.Fatal("nothing may be deleted without confirmation")
	}
	if len(f.reqs) != 0 {
		t.Fatalf("no request expected, got %d", len(f.reqs))
	}
	capture(t, func() { err = run(t, mustCmd(t, "branch delete"), []string{"fix/x"}, f2("yes", "true")) })
	if err != nil || len(f.reqs) != 1 || f.reqs[0].Method != "DELETE" {
		t.Fatalf("with --yes: %v %+v", err, f.reqs)
	}
}

func TestShortIDsAndOutput(t *testing.T) {
	runs := `{"items":[{"id":"9c1d2e3f-aaaa-4bbb-8ccc-000000000001","status":"running","workflow":"CI","branch":"main","event":"push","sha":"abcdef1234","actor":{"login":"ana"},"created_at":"2026-11-01T10:00:00Z"},{"id":"9c1d99aa-aaaa-4bbb-8ccc-000000000002","status":"success","workflow":"CI","branch":"dev","event":"push","sha":"1234567890"}],"next_cursor":null}`
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET " + gitBase() + "/runs": respond(200, runs),
		"POST " + gitBase() + "/runs/9c1d2e3f-aaaa-4bbb-8ccc-000000000001/cancel": respond(200, `{}`),
	})
	var err error
	capture(t, func() { err = run(t, mustCmd(t, "runs cancel"), []string{"9c1d2e3f"}, f2("yes", "true")) })
	if err != nil || f.count("POST", "000000000001/cancel") != 1 {
		t.Fatalf("short id: %v", err)
	}
	capture(t, func() { err = run(t, mustCmd(t, "runs cancel"), []string{"9c1d"}, f2("yes", "true")) })
	if exitCodeFor(err) != exitUsage || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix must be a usage error: %v", err)
	}
	// table
	out, err := runOut(t, mustCmd(t, "runs list"), nil, nil)
	if err != nil || !strings.Contains(out, "ID") || !strings.Contains(out, "9c1d2e3f") || !strings.Contains(out, "ana") {
		t.Fatalf("table: %v\n%s", err, out)
	}
	out, _ = runOutM(t, mustCmd(t, "runs list"), nil, f2("no-header", "true"))
	if strings.Contains(out, "STATUS") {
		t.Fatalf("--no-header: %s", out)
	}
	out, _ = runOut(t, mustCmd(t, "runs list"), nil, f2("quiet", "true"))
	if strings.Contains(out, "STATUS") || !strings.Contains(out, "9c1d2e3f-aaaa-4bbb-8ccc-000000000001") {
		t.Fatalf("-q prints ids: %s", out)
	}
	// json
	_ = rootCmd.PersistentFlags().Set("output", "json")
	out, _ = runOut(t, mustCmd(t, "runs list"), nil, nil)
	_ = rootCmd.PersistentFlags().Set("output", "table")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil || parsed["items"] == nil {
		t.Fatalf("--output json must print the API object: %v\n%s", err, out)
	}
	// jq subset
	out, err = runOutM(t, mustCmd(t, "runs list"), nil, f2("jq", ".items[].status"))
	if err != nil || strings.TrimSpace(out) != "running\nsuccess" {
		t.Fatalf("--jq: %v %q", err, out)
	}
	if _, err := runOutM(t, mustCmd(t, "runs list"), nil, f2("jq", "map(.x)")); exitCodeFor(err) != exitUsage {
		t.Fatalf("unsupported jq must be a usage error: %v", err)
	}
}

func TestJQSubset(t *testing.T) {
	v := map[string]any{"a": map[string]any{"b": []any{1.0, "x"}}, "n": nil}
	for expr, want := range map[string]string{
		".a.b[0]":    `[1]`,
		".a.b[]":     `[1,"x"]`,
		".a.b[-1]":   `["x"]`,
		".a | .b[1]": `["x"]`,
		".n":         `[null]`,
	} {
		got, err := jqEval(expr, v)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		b, _ := json.Marshal(got)
		if string(b) != want {
			t.Errorf("%s -> %s, want %s", expr, b, want)
		}
	}
	for _, bad := range []string{"keys", "map(.x)", ".a[", ".a.b[x]"} {
		if _, err := jqEval(bad, v); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestWaitTimeoutAndExitStatus(t *testing.T) {
	old := pollInterval
	pollInterval = 5 * time.Millisecond
	defer func() { pollInterval = old }()
	status := "running"
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET " + gitBase() + "/runs/77": func(w http.ResponseWriter, r *http.Request) {
			respond(200, `{"id":"77","status":"`+status+`"}`)(w, r)
		},
	})
	_ = f
	var err error
	capture(t, func() { err = run(t, mustCmd(t, "runs watch"), []string{"77"}, f2("wait", "40ms")) })
	if exitCodeFor(err) != exitTimeout {
		t.Fatalf("timeout must be exit 6: %v", err)
	}
	status = "failure"
	capture(t, func() {
		err = run(t, mustCmd(t, "runs watch"), []string{"77"}, f2("exit-status", "true", "wait", "1s"))
	})
	if exitCodeFor(err) != exitTimeout {
		t.Fatalf("failed run with --exit-status must be exit 6: %v", err)
	}
	capture(t, func() { err = run(t, mustCmd(t, "runs watch"), []string{"77"}, f2("wait", "1s")) })
	if err != nil {
		t.Fatalf("a failed run without --exit-status still exits 0: %v", err)
	}
	status = "success"
	capture(t, func() {
		err = run(t, mustCmd(t, "runs watch"), []string{"77"}, f2("exit-status", "true", "wait", "1s"))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPromoteWaitPendingApprovalIsExit8(t *testing.T) {
	old := pollInterval
	pollInterval = 5 * time.Millisecond
	defer func() { pollInterval = old }()
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST " + projURL() + "/promotions":         respond(202, `{"id":"`+uuidA+`","environment":"production","status":"pending_approval","required_approvals":1}`),
		"GET " + projURL() + "/promotions/" + uuidA: respond(200, `{"id":"`+uuidA+`","environment":"production","sha":"a1b2c3d","status":"pending_approval","required_approvals":1,"approvals":[]}`),
	})
	var err error
	out := capture(t, func() {
		err = run(t, mustCmd(t, "promote"), nil, f2("to", "production", "sha", "a1b2c3d", "wait", "1s"))
	})
	if exitCodeFor(err) != exitApproval {
		t.Fatalf("pending approval must be exit 8: %v", err)
	}
	if !strings.Contains(out, "pending_approval") || f.find("POST", "/promotions").Header.Get("Idempotency-Key") == "" {
		t.Fatalf("output %q / missing idempotency key", out)
	}
}

func TestIdempotencyKeyIsStableAndOverridable(t *testing.T) {
	c := mustCmd(t, "pr merge")
	k1 := idemHeaders(c, []string{"12"})["Idempotency-Key"]
	k2 := idemHeaders(c, []string{"12"})["Idempotency-Key"]
	k3 := idemHeaders(c, []string{"13"})["Idempotency-Key"]
	if k1 == "" || k1 != k2 || k1 == k3 {
		t.Fatalf("keys: %q %q %q", k1, k2, k3)
	}
	_ = c.Flags().Set("idempotency-key", "mine-1")
	defer c.Flags().Set("idempotency-key", "")
	if got := idemHeaders(c, []string{"12"})["Idempotency-Key"]; got != "mine-1" {
		t.Fatalf("explicit key: %q", got)
	}
}

func TestSecretsComeFromStdinOrEnvNeverArgv(t *testing.T) {
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"PUT " + gitBase() + "/secrets/DB_PASS": respond(204, ""),
	})
	var err error
	restore := withStdin(t, "s3cr3t-value\n")
	out := capture(t, func() { err = run(t, mustCmd(t, "ci secrets set"), []string{"DB_PASS"}, nil) })
	restore()
	if err != nil || strings.Contains(out, "s3cr3t") {
		t.Fatalf("%v %q", err, out)
	}
	if b := f.find("PUT", "/secrets/DB_PASS").Body; b["value"] != "s3cr3t-value" {
		t.Fatalf("body %v", b)
	}
	t.Setenv("MY_SECRET_ENV", "from-env-value")
	capture(t, func() {
		err = run(t, mustCmd(t, "ci secrets set"), []string{"DB_PASS"}, f2("from-env", "MY_SECRET_ENV"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if mustCmd(t, "ci secrets set").Flags().Lookup("value") != nil {
		t.Fatal("secrets must not have a --value flag (argv is visible)")
	}
	if mustCmd(t, "ci vars set").Flags().Lookup("value") == nil {
		t.Fatal("variables may use --value")
	}
	capture(t, func() { err = run(t, mustCmd(t, "ci secrets set"), []string{"1BAD"}, f2("from-env", "MY_SECRET_ENV")) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("bad name: %v", err)
	}
}

func TestNewTokenValuePrintedOnceOnStdout(t *testing.T) {
	newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/user/git-tokens": respond(201, `{"id":"`+uuidA+`","name":"ci","token":"cgt_ABCDEF","expires_at":"2099-01-01T00:00:00Z"}`),
	})
	var err error
	out := capture(t, func() {
		err = run(t, mustCmd(t, "git tokens create"), nil, f2("name", "ci", "scope", "write", "expires", "7d"))
	})
	if err != nil || strings.TrimSpace(out) != "cgt_ABCDEF" {
		t.Fatalf("stdout must carry only the value: %v %q", err, out)
	}
}

func TestSarifUploadCompressesAndEncodes(t *testing.T) {
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST " + projURL() + "/security/sarif": respond(202, `{"id":"`+uuidA+`","status":"pending"}`),
	})
	file := filepath.Join(t.TempDir(), "x.sarif")
	content := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"semgrep"}},"results":[]}]}`
	_ = os.WriteFile(file, []byte(content), 0o600)
	var err error
	capture(t, func() {
		err = run(t, mustCmd(t, "security sarif upload"), []string{file}, f2("sha", "abc", "ref", "main", "tool", "semgrep", "pr", "4"))
	})
	if err != nil {
		t.Fatal(err)
	}
	b := f.find("POST", "/security/sarif").Body
	raw, derr := base64.StdEncoding.DecodeString(b["sarif"].(string))
	if derr != nil {
		t.Fatal(derr)
	}
	zr, derr := gzip.NewReader(bytes.NewReader(raw))
	if derr != nil {
		t.Fatal(derr)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != content || b["commit_sha"] != "abc" || b["ref"] != "main" || b["tool_name"] != "semgrep" || b["pr"] != float64(4) {
		t.Fatalf("body %v / %s", b, got)
	}
	notSarif := filepath.Join(t.TempDir(), "n.json")
	_ = os.WriteFile(notSarif, []byte(`{"a":1}`), 0o600)
	capture(t, func() {
		err = run(t, mustCmd(t, "security sarif upload"), []string{notSarif}, f2("sha", "abc", "ref", "main"))
	})
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("non-SARIF must be rejected: %v", err)
	}
}

func TestDismissSendsExpiryAndJustification(t *testing.T) {
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST " + projURL() + "/security/alerts/" + uuidA + "/dismiss": respond(200, `{}`),
	})
	var err error
	capture(t, func() {
		err = run(t, mustCmd(t, "security alerts dismiss"), []string{uuidA}, f2("reason", "false-positive", "why", "test fixture only", "expires", "90d"))
	})
	if err != nil {
		t.Fatal(err)
	}
	b := f.find("POST", "/dismiss").Body
	exp, perr := time.Parse(time.RFC3339, b["expires_at"].(string))
	if perr != nil || exp.Before(time.Now().Add(89*24*time.Hour)) || b["reason"] != "false_positive" || b["justification"] != "test fixture only" {
		t.Fatalf("%v %v", b, perr)
	}
}

func TestProtectSetMergesExistingRule(t *testing.T) {
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET " + gitBase() + "/protections":          respond(200, `{"items":[{"id":"`+uuidA+`","pattern":"main","require_pull_request":true,"required_approvals":1,"block_force_push":true,"forgejo_synced":true,"created_at":"x"}]}`),
		"PUT " + gitBase() + "/protections/" + uuidA: respond(200, `{"id":"`+uuidA+`","pattern":"main","forgejo_synced":true}`),
	})
	var err error
	out := capture(t, func() {
		err = run(t, mustCmd(t, "git protect set"), []string{"main"}, f2("approvals", "2", "codeowners", "true"))
	})
	if err != nil || !strings.Contains(out, "forgejo_synced=true") {
		t.Fatalf("%v %s", err, out)
	}
	b := f.find("PUT", "/protections/"+uuidA).Body
	if b["required_approvals"] != float64(2) || b["require_code_owner_review"] != true || b["require_pull_request"] != true || b["block_force_push"] != true {
		t.Fatalf("merged body: %v", b)
	}
	for _, k := range []string{"id", "created_at", "forgejo_synced"} {
		if _, ok := b[k]; ok {
			t.Fatalf("server-owned field %q must not be sent", k)
		}
	}
}

func TestEnvironmentsParseWindowsAndReviewers(t *testing.T) {
	w, err := parseWindow("mon-fri 09:00-18:00 America/Lima")
	if err != nil || w["from"] != "09:00" || w["tz"] != "America/Lima" || len(w["days"].([]string)) != 5 {
		t.Fatalf("%v %v", w, err)
	}
	if w, _ := parseWindow("sat,sun 00:00-23:59"); w["tz"] != "UTC" {
		t.Fatalf("%v", w)
	}
	for _, bad := range []string{"", "mon", "fri-mon 09:00-18:00", "mon 9-18", "mon 25:00-26:00"} {
		if _, err := parseWindow(bad); exitCodeFor(err) != exitUsage {
			t.Errorf("%q should be a usage error: %v", bad, err)
		}
	}
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST " + projURL() + "/environments": respond(201, `{}`),
	})
	capture(t, func() {
		err = run(t, mustCmd(t, "environments create"), []string{"production"}, f2("kind", "production", "reviewers", "role:admin,user:"+uuidB, "approvals", "1", "window", "mon-fri 09:00-18:00 America/Lima", "branches", "main"))
	})
	if err != nil {
		t.Fatal(err)
	}
	b := f.find("POST", "/environments").Body
	rv := b["reviewers"].([]any)
	if len(rv) != 2 || rv[0].(map[string]any)["role"] != "admin" || rv[1].(map[string]any)["user_id"] != uuidB || b["name"] != "production" {
		t.Fatalf("%v", b)
	}
	capture(t, func() { err = run(t, mustCmd(t, "environments create"), []string{"Bad_Name"}, f2("kind", "other")) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("invalid name: %v", err)
	}
}

func TestApplyAPICommand(t *testing.T) {
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/orgs/x/things":  respond(200, `{"ok":true}`),
		"POST /api/v1/orgs/x/things": respond(200, `{"made":true}`),
	})
	var err error
	out := capture(t, func() {
		err = run(t, mustCmd(t, "api"), []string{"get", "/orgs/x/things"}, f2("raw-field", "a=1", "field", "n=2"))
	})
	if err != nil || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("%v %s", err, out)
	}
	if q := f.reqs[0].Query; q != "a=1&n=2" {
		t.Fatalf("GET fields go in the query: %q", q)
	}
	capture(t, func() {
		err = run(t, mustCmd(t, "api"), []string{"POST", "/orgs/x/things"}, f2("raw-field", "name=n", "field", "draft=true", "field", "n=3"))
	})
	b := f.reqs[1].Body
	if err != nil || b["name"] != "n" || b["draft"] != true || b["n"] != float64(3) {
		t.Fatalf("%v %v", err, b)
	}
	for _, args := range [][]string{{"GET", "https://evil.example.com/x"}, {"TRACE", "/x"}} {
		capture(t, func() { err = run(t, mustCmd(t, "api"), args, nil) })
		if exitCodeFor(err) != exitUsage {
			t.Errorf("%v should be a usage error: %v", args, err)
		}
	}
	capture(t, func() {
		err = run(t, mustCmd(t, "api"), []string{"GET", "/orgs/x/things"}, f2("header", "Authorization: Bearer stolen"))
	})
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("Authorization header override must be refused: %v", err)
	}
}

func TestAliasesExpand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CAPSULE_TOKEN", "")
	var err error
	capture(t, func() { err = run(t, mustCmd(t, "alias set"), []string{"mine", "pr list --author @me"}, nil) })
	if err != nil {
		t.Fatal(err)
	}
	capture(t, func() { err = run(t, mustCmd(t, "alias set"), []string{"co", "pr co $1 --remote $2"}, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if got := expandAliases([]string{"mine", "--limit", "5"}); strings.Join(got, " ") != "pr list --author @me --limit 5" {
		t.Fatalf("%v", got)
	}
	if got := expandAliases([]string{"co", "7", "origin", "-x"}); strings.Join(got, " ") != "pr co 7 --remote origin -x" {
		t.Fatalf("%v", got)
	}
	if got := expandAliases([]string{"pr", "list"}); strings.Join(got, " ") != "pr list" {
		t.Fatalf("built-ins are never rewritten: %v", got)
	}
	capture(t, func() { err = run(t, mustCmd(t, "alias set"), []string{"pr", "version"}, nil) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("shadowing a built-in must be refused: %v", err)
	}
	capture(t, func() { err = run(t, mustCmd(t, "alias set"), []string{"sh", "!rm -rf /"}, nil) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("shell aliases must be refused: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(home, ".capsule", "config.yaml")); serr != nil {
		t.Fatal("aliases must be saved in ~/.capsule/config.yaml")
	}
	capture(t, func() { err = run(t, mustCmd(t, "alias delete"), []string{"mine"}, nil) })
	if err != nil || strings.Contains(strings.Join(expandAliases([]string{"mine"}), " "), "pr") {
		t.Fatalf("delete: %v", err)
	}
	words, perr := splitArgs(`a "b c" 'd e' f\ g`)
	if perr != nil || len(words) != 4 || words[1] != "b c" || words[3] != "f g" {
		t.Fatalf("%v %v", words, perr)
	}
}

func TestWorkflowTemplatesAreWritten(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPSULE_CWD", dir)
	var err error
	for _, tpl := range []string{"ci-node", "sast", "gitleaks", "trivy"} {
		capture(t, func() { err = run(t, mustCmd(t, "workflow new"), nil, f2("template", tpl)) })
		if err != nil {
			t.Fatalf("%s: %v", tpl, err)
		}
		b, rerr := os.ReadFile(filepath.Join(dir, ".forgejo", "workflows", tpl+".yml"))
		if rerr != nil || !strings.Contains(string(b), "runs-on: docker") {
			t.Fatalf("%s: %v", tpl, rerr)
		}
		if tpl != "ci-node" && !strings.Contains(string(b), "capsule security sarif upload") {
			t.Fatalf("%s must upload SARIF", tpl)
		}
	}
	capture(t, func() { err = run(t, mustCmd(t, "workflow new"), nil, f2("template", "sast")) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("overwrite needs --force: %v", err)
	}
	capture(t, func() { err = run(t, mustCmd(t, "workflow new"), nil, f2("template", "nope")) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("unknown template: %v", err)
	}
}

func TestRollbackTarget(t *testing.T) {
	d := func(id, status, at string) deploymentsAPIItem {
		return deploymentsAPIItem{ID: id, Status: status, CreatedAt: at}
	}
	cases := []struct {
		name  string
		items []deploymentsAPIItem
		want  string
	}{
		{"latest succeeded: previous success", []deploymentsAPIItem{d("c", "success", "2026-11-03"), d("b", "success", "2026-11-02"), d("a", "success", "2026-11-01")}, "b"},
		{"latest succeeded, previous failed: skip the failure", []deploymentsAPIItem{d("c", "success", "2026-11-03"), d("b", "failed", "2026-11-02"), d("a", "success", "2026-11-01")}, "a"},
		{"latest failed: last success (the running one)", []deploymentsAPIItem{d("c", "failed", "2026-11-03"), d("b", "success", "2026-11-02"), d("a", "success", "2026-11-01")}, "b"},
		{"two failures then success", []deploymentsAPIItem{d("d", "failed", "2026-11-04"), d("c", "failed", "2026-11-03"), d("b", "success", "2026-11-02"), d("a", "success", "2026-11-01")}, "b"},
		{"latest still building: success before the running one", []deploymentsAPIItem{d("c", "building", "2026-11-03"), d("b", "success", "2026-11-02"), d("a", "success", "2026-11-01")}, "a"},
		{"unordered input", []deploymentsAPIItem{d("a", "success", "2026-11-01"), d("c", "success", "2026-11-03"), d("b", "success", "2026-11-02")}, "b"},
		{"only one success and it is current", []deploymentsAPIItem{d("b", "success", "2026-11-02"), d("a", "failed", "2026-11-01")}, ""},
		{"only failures", []deploymentsAPIItem{d("b", "failed", "2026-11-02")}, ""},
		{"latest failed, single success", []deploymentsAPIItem{d("b", "failed", "2026-11-02"), d("a", "success", "2026-11-01")}, "a"},
	}
	for _, c := range cases {
		got := pickRollbackTarget(c.items)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: want none, got %s", c.name, got.ID)
		case c.want != "" && (got == nil || got.ID != c.want):
			t.Errorf("%s: want %s, got %v", c.name, c.want, got)
		}
	}
}

func TestRepoCloneAndGitAliasesExist(t *testing.T) {
	for _, words := range []string{"repo ls", "repo cat", "repo view", "repo create", "repo clone", "git previews list", "git branches list", "ci vars list", "environments list", "envs list", "pulls list", "run list", "exit-codes"} {
		if c, rest, err := rootCmd.Find(strings.Fields(words)); err != nil || c == rootCmd || len(rest) > 0 {
			t.Errorf("missing command %q (%v)", words, err)
		}
	}
	var _ = config.Config{}
}
