package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kynto-consulting/capsule/cli/internal/client"
	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// Real-Git tests for the credential helper and `capsule git clone`/`login`.
//
// A TLS test server plays both parts of Git Capsule:
//   - the Capsule API: POST /api/v1/user/git-credentials issues short-lived
//     cgt_ tokens scoped to one repository (read-only role gets 403 on write,
//     unknown repositories 404), validates the CLI session token and refresh;
//   - the Git host: Basic auth is checked against those tokens (scope, repo,
//     expiry) and then handed to `git http-backend` over CGI, so clone, fetch
//     and push are real smart-HTTP.
// Everything runs with an isolated HOME and git config, never touching the
// developer's own.
//
// By default the helper is the natively built capsule binary. Set
// CAPSULE_HELPER_CMD to a shell command (for example
// `node /path/npm/bin/capsule.js`) to run the same scenarios through the WASM
// build and the Node wrapper.

type issued struct {
	token, user, repo, scope string
	expires                  time.Time
}

type gitSim struct {
	t         *testing.T
	mu        sync.Mutex
	tokens    map[string]issued
	issuedLog []issued
	session   string // the only valid CLI session token
	ttl       time.Duration
	api, git  *httptest.Server
	root      string // bare repos: root/org/repo.git
	authSeen  []string
	denied    int
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func fakeJWT(exp time.Time) string {
	h := b64url([]byte(`{"alg":"none"}`))
	p := b64url([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	var r [6]byte
	_, _ = rand.Read(r[:])
	return h + "." + p + "." + hex.EncodeToString(r[:])
}

func gitEnvOK(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func backendPath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git --exec-path failed")
	}
	p := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("git-http-backend not available")
	}
	return p
}

func isolatedGit(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Ana")
	t.Setenv("GIT_AUTHOR_EMAIL", "ana@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Ana")
	t.Setenv("GIT_COMMITTER_EMAIL", "ana@example.com")
	t.Setenv("CAPSULE_TOKEN", "")
	t.Setenv("CAPSULE_API_URL", "")
	return home
}

func git(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.String(), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func newGitSim(t *testing.T) *gitSim {
	t.Helper()
	gitEnvOK(t)
	backend := backendPath(t)
	s := &gitSim{t: t, tokens: map[string]issued{}, session: fakeJWT(time.Now().Add(time.Hour)), ttl: time.Hour, root: t.TempDir()}
	for _, repo := range []string{"org/repo", "ro/repo"} {
		bare := filepath.Join(s.root, filepath.FromSlash(repo)+".git")
		mustGit(t, "", "init", "--bare", "-b", "main", bare)
		mustGit(t, bare, "config", "http.receivepack", "true")
		work := t.TempDir()
		mustGit(t, work, "init", "-b", "main")
		_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("hello "+repo+"\n"), 0o644)
		mustGit(t, work, "add", ".")
		mustGit(t, work, "commit", "-m", "init")
		mustGit(t, work, "push", bare, "main")
	}
	s.api = httptest.NewServer(http.HandlerFunc(s.apiHandler))
	t.Cleanup(s.api.Close)
	cgiH := &cgi.Handler{
		Path:       backend,
		Root:       "",
		Dir:        s.root,
		Env:        []string{"GIT_PROJECT_ROOT=" + s.root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=ana"},
		InheritEnv: []string{"PATH", "SystemRoot", "SYSTEMROOT", "TEMP", "TMP", "HOME", "USERPROFILE", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"},
	}
	s.git = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorize(w, r) {
			return
		}
		cgiH.ServeHTTP(w, r)
	}))
	t.Cleanup(s.git.Close)
	return s
}

func repoOf(urlPath string) string {
	p := strings.TrimPrefix(urlPath, "/")
	if i := strings.Index(p, ".git"); i >= 0 {
		return p[:i]
	}
	return p
}

func (s *gitSim) authorize(w http.ResponseWriter, r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	deny := func(code int) bool {
		s.mu.Lock()
		s.denied++
		s.mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		http.Error(w, "auth", code)
		return false
	}
	if !ok {
		return deny(401)
	}
	s.mu.Lock()
	tk, found := s.tokens[pass]
	s.authSeen = append(s.authSeen, user+":"+pass[:min(8, len(pass))])
	s.mu.Unlock()
	if !found || user != tk.user || time.Now().After(tk.expires) || tk.repo != repoOf(r.URL.Path) {
		return deny(401)
	}
	write := strings.HasSuffix(r.URL.Path, "git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack"
	if write && tk.scope != "write" {
		return deny(403)
	}
	return true
}

func (s *gitSim) apiHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + strings.ToLower(code) + `"}}`))
	}
	switch {
	case r.URL.Path == "/api/v1/auth/refresh":
		var in struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.RefreshToken != "refresh-ok" {
			fail(401, "UNAUTHORIZED")
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"` + s.session + `","refresh_token":"refresh-ok"}`))
	case r.URL.Path == "/api/v1/auth/me":
		if r.Header.Get("Authorization") != "Bearer "+s.session {
			fail(401, "UNAUTHORIZED")
			return
		}
		_, _ = w.Write([]byte(`{"email":"ana@example.com"}`))
	case r.URL.Path == "/api/v1/user/git-credentials" && r.Method == "POST":
		if r.Header.Get("Authorization") != "Bearer "+s.session {
			fail(401, "UNAUTHORIZED")
			return
		}
		var in struct{ Protocol, Host, Path, Scope string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		repo := strings.TrimSuffix(strings.TrimPrefix(in.Path, "/"), ".git")
		if in.Protocol != "https" || (repo != "org/repo" && repo != "ro/repo") {
			fail(404, "NOT_FOUND")
			return
		}
		if in.Scope == "write" && repo == "ro/repo" {
			fail(403, "FORBIDDEN")
			return
		}
		var rnd [16]byte
		_, _ = rand.Read(rnd[:])
		tk := issued{token: "cgt_" + hex.EncodeToString(rnd[:]), user: "ana", repo: repo, scope: in.Scope, expires: time.Now().Add(s.ttl)}
		s.mu.Lock()
		s.tokens[tk.token] = tk
		s.issuedLog = append(s.issuedLog, tk)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"username": tk.user, "password": tk.token, "expires_at": tk.expires.UTC().Format(time.RFC3339), "scope": tk.scope})
	default:
		fail(404, "NOT_FOUND")
	}
}

func (s *gitSim) issuedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.issuedLog)
}

func (s *gitSim) allTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, i := range s.issuedLog {
		out = append(out, i.token)
	}
	return out
}

// writeSession stores a CLI session as `capsule login` would.
func writeSession(t *testing.T, home, apiURL, token, refresh string) {
	t.Helper()
	dir := filepath.Join(home, ".capsule")
	_ = os.MkdirAll(dir, 0o700)
	body := fmt.Sprintf("api_url: %s\ntoken: %s\nrefresh_token: %s\n", apiURL, token, refresh)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// helperCommand returns the value for credential.<url>.helper and, when the
// native binary is used, the path to it.
func helperCommand(t *testing.T) (helper string, bin []string) {
	t.Helper()
	if h := os.Getenv("CAPSULE_HELPER_CMD"); h != "" {
		return "!" + h + " git credential", strings.Fields(h)
	}
	exe := builtBinary(t)
	p := filepath.ToSlash(exe)
	return `!"` + p + `" git credential`, []string{exe}
}

func runHelper(t *testing.T, bin []string, stdin string, env map[string]string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	c := helperExec(bin, args...)
	c.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	c.Stdout, c.Stderr = &o, &e
	c.Env = os.Environ()
	for k, v := range env {
		c.Env = append(c.Env, k+"="+v)
	}
	err := c.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the helper: %v", err)
	}
	return o.String(), e.String(), code
}

func assertNoTokenOnDisk(t *testing.T, s *gitSim, dirs ...string) {
	t.Helper()
	toks := s.allTokens()
	if len(toks) == 0 {
		t.Fatal("no tokens were issued: the helper never ran")
	}
	for _, d := range dirs {
		_ = filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Size() > 5<<20 {
				return nil
			}
			b, _ := os.ReadFile(p)
			for _, tk := range toks {
				if bytes.Contains(b, []byte(tk)) {
					t.Errorf("a cgt_ token was written to disk: %s", p)
				}
			}
			return nil
		})
	}
}

func TestCredentialHelperWithRealGit(t *testing.T) {
	home := isolatedGit(t)
	s := newGitSim(t)
	helper, _ := helperCommand(t)
	writeSession(t, home, s.api.URL, s.session, "refresh-ok")
	hostURL := s.git.URL // https://127.0.0.1:PORT

	// `capsule git login` writes the helper config (idempotent).
	apiOld, cfgOld := apiClient, cfg
	defer func() { apiClient, cfg = apiOld, cfgOld }()
	cfg = &config.Config{Token: s.session}
	apiClient = newClientFor(s.api.URL, s.session)
	login := mustCmd(t, "git login")
	for i := 0; i < 2; i++ {
		var err error
		capture(t, func() {
			err = run(t, login, nil, f2("host", hostURL, "helper", helper, "no-cache", "true"))
		})
		if err != nil {
			t.Fatalf("git login: %v", err)
		}
	}
	got := mustGit(t, "", "config", "--global", "--get-all", "credential."+hostURL+".helper")
	if lines := strings.Split(strings.TrimRight(got, "\r\n"), "\n"); len(lines) != 2 || lines[0] != "" || !strings.Contains(lines[1], "git credential") {
		t.Fatalf("helper config after two logins must have the reset line and the helper once: %q", got)
	}
	if v := strings.TrimSpace(mustGit(t, "", "config", "--global", "credential."+hostURL+".useHttpPath")); v != "true" {
		t.Fatalf("useHttpPath: %q", v)
	}

	// clone (read) through the helper
	work := t.TempDir()
	clone := filepath.Join(work, "repo")
	mustGit(t, work, "clone", hostURL+"/org/repo.git", clone)
	if b, _ := os.ReadFile(filepath.Join(clone, "README.md")); !strings.Contains(string(b), "hello org/repo") {
		t.Fatalf("clone content: %q", b)
	}
	cfgText, _ := os.ReadFile(filepath.Join(clone, ".git", "config"))
	if strings.Contains(string(cfgText), "@") || strings.Contains(string(cfgText), "cgt_") {
		t.Fatalf("no credentials may be written to .git/config: %s", cfgText)
	}
	if s.issuedCount() == 0 || !strings.HasPrefix(s.authSeen[len(s.authSeen)-1], "ana:cgt_") {
		t.Fatalf("Git must authenticate as ana with a cgt_ token: %v", s.authSeen)
	}

	// push (write) works for a deployer role
	mustGit(t, clone, "checkout", "-b", "fix/plazos")
	_ = os.WriteFile(filepath.Join(clone, "plazo.txt"), []byte("x\n"), 0o644)
	mustGit(t, clone, "add", ".")
	mustGit(t, clone, "commit", "-m", "feat: plazo")
	mustGit(t, clone, "push", "origin", "fix/plazos")
	if out := mustGit(t, filepath.Join(s.root, "org"), "--git-dir=repo.git", "branch", "--list", "fix/plazos"); !strings.Contains(out, "fix/plazos") {
		t.Fatalf("the push did not arrive: %q", out)
	}

	// read-only role: clone works (falls back to read), push is refused
	ro := filepath.Join(work, "ro")
	mustGit(t, work, "clone", hostURL+"/ro/repo.git", ro)
	_ = os.WriteFile(filepath.Join(ro, "x.txt"), []byte("x\n"), 0o644)
	mustGit(t, ro, "add", ".")
	mustGit(t, ro, "commit", "-m", "x")
	if out, err := git(t, ro, "push", "origin", "main"); err == nil {
		t.Fatalf("a read-only token must not push:\n%s", out)
	}

	// a repository the user cannot see: authentication fails, Git does not prompt
	if out, err := git(t, work, "ls-remote", hostURL+"/secret/repo.git"); err == nil {
		t.Fatalf("unknown repo must fail:\n%s", out)
	}

	// `git credential fill` speaks the protocol with the helper
	fill := exec.Command("git", "credential", "fill")
	fill.Stdin = strings.NewReader("protocol=https\nhost=" + strings.TrimPrefix(hostURL, "https://") + "\npath=org/repo.git\n\n")
	var fillOut bytes.Buffer
	fill.Stdout = &fillOut
	fill.Stderr = io.Discard
	if err := fill.Run(); err != nil || !strings.Contains(fillOut.String(), "username=ana") || !strings.Contains(fillOut.String(), "password=cgt_") {
		t.Fatalf("git credential fill: %v\n%s", err, strings.ReplaceAll(fillOut.String(), "cgt_", "cgt_<hidden>"))
	}

	assertNoTokenOnDisk(t, s, home, work)
}

func TestCredentialHelperRenewsExpiredTokens(t *testing.T) {
	home := isolatedGit(t)
	s := newGitSim(t)
	s.ttl = 1500 * time.Millisecond
	helper, _ := helperCommand(t)
	writeSession(t, home, s.api.URL, s.session, "refresh-ok")
	mustGit(t, "", "config", "--global", "credential."+s.git.URL+".helper", "")
	mustGit(t, "", "config", "--global", "--add", "credential."+s.git.URL+".helper", helper)
	mustGit(t, "", "config", "--global", "credential."+s.git.URL+".useHttpPath", "true")
	work := t.TempDir()
	mustGit(t, work, "ls-remote", s.git.URL+"/org/repo.git")
	first := s.issuedCount()
	time.Sleep(2 * time.Second) // the first token is now expired on the server
	mustGit(t, work, "ls-remote", s.git.URL+"/org/repo.git")
	if s.issuedCount() <= first {
		t.Fatal("a new token must be issued for the next Git command once the old one expired")
	}
	assertNoTokenOnDisk(t, s, home)
}

func TestCredentialHelperRefreshesTheCLISession(t *testing.T) {
	home := isolatedGit(t)
	s := newGitSim(t)
	helper, _ := helperCommand(t)
	// expired access token + valid refresh token: the helper refreshes like any command
	expired := fakeJWT(time.Now().Add(-time.Hour))
	writeSession(t, home, s.api.URL, expired, "refresh-ok")
	mustGit(t, "", "config", "--global", "credential."+s.git.URL+".helper", "")
	mustGit(t, "", "config", "--global", "--add", "credential."+s.git.URL+".helper", helper)
	mustGit(t, "", "config", "--global", "credential."+s.git.URL+".useHttpPath", "true")
	mustGit(t, t.TempDir(), "ls-remote", s.git.URL+"/org/repo.git")
	cfgText, _ := os.ReadFile(filepath.Join(home, ".capsule", "config.yaml"))
	if strings.Contains(string(cfgText), expired) || !strings.Contains(string(cfgText), s.session) {
		t.Fatalf("the refreshed session must be saved by the existing refresh logic:\n%s", cfgText)
	}
}

func TestCredentialHelperExitCodesAndNoStorage(t *testing.T) {
	home := isolatedGit(t)
	s := newGitSim(t)
	_, bin := helperCommand(t)
	req := func(path string) string {
		return "protocol=https\nhost=" + strings.TrimPrefix(s.git.URL, "https://") + "\npath=" + path + "\n\n"
	}

	// no session -> exit 3, nothing on stdout, a hint on stderr
	out, errOut, code := runHelper(t, bin, req("org/repo.git"), nil, "git", "credential", "get")
	if code != exitAuth || out != "" || !strings.Contains(errOut, "capsule login") {
		t.Fatalf("no session: code=%d out=%q err=%q", code, out, errOut)
	}
	// session present, repository hidden -> exit 4, empty stdout (Git falls through)
	writeSession(t, home, s.api.URL, s.session, "refresh-ok")
	out, errOut, code = runHelper(t, bin, req("secret/repo.git"), nil, "git", "credential", "get")
	if code != exitDenied || out != "" {
		t.Fatalf("hidden repo: code=%d out=%q err=%q", code, out, errOut)
	}
	// invalid session and no refresh token -> exit 3
	writeSession(t, home, s.api.URL, "bad-session", "")
	out, _, code = runHelper(t, bin, req("org/repo.git"), nil, "git", "credential", "get")
	if code != exitAuth || out != "" {
		t.Fatalf("bad session: code=%d out=%q", code, out)
	}
	// a Git service token in CAPSULE_TOKEN is refused: exit 2
	writeSession(t, home, s.api.URL, s.session, "refresh-ok")
	out, errOut, code = runHelper(t, bin, req("org/repo.git"), map[string]string{"CAPSULE_TOKEN": "cgt_servicetoken", "CAPSULE_API_URL": s.api.URL}, "git", "credential", "get")
	if code != exitUsage || out != "" || strings.Contains(errOut, "cgt_servicetoken") {
		t.Fatalf("service token: code=%d out=%q err=%q", code, out, errOut)
	}
	// non-https protocols: no answer, exit 0
	out, _, code = runHelper(t, bin, "protocol=ssh\nhost=x\n\n", nil, "git", "credential", "get")
	if code != 0 || out != "" {
		t.Fatalf("ssh: code=%d out=%q", code, out)
	}
	// a good get prints exactly the protocol lines
	out, errOut, code = runHelper(t, bin, req("org/repo.git"), nil, "git", "credential", "get")
	lines := strings.Split(strings.TrimRight(out, "\r\n"), "\n")
	if code != 0 || len(lines) != 3 || lines[0] != "username=ana" || !strings.HasPrefix(lines[1], "password=cgt_") || !strings.HasPrefix(lines[2], "password_expiry_utc=") || errOut != "" {
		t.Fatalf("get: code=%d out=%q err=%q", code, out, errOut)
	}
	// store and erase are no-ops: exit 0, no output, nothing persisted
	before, _ := os.ReadFile(filepath.Join(home, ".capsule", "config.yaml"))
	for _, action := range []string{"store", "erase"} {
		out, errOut, code = runHelper(t, bin, req("org/repo.git")+"", nil, "git", "credential", action)
		if code != 0 || out != "" || errOut != "" {
			t.Fatalf("%s: code=%d out=%q err=%q", action, code, out, errOut)
		}
	}
	out, _, code = runHelper(t, bin, "protocol=https\nhost=x\npath=org/repo.git\nusername=ana\npassword=cgt_LEAKME\n\n", nil, "git", "credential", "store")
	after, _ := os.ReadFile(filepath.Join(home, ".capsule", "config.yaml"))
	if code != 0 || string(before) != string(after) || bytes.Contains(after, []byte("LEAKME")) {
		t.Fatalf("store must not write anything (code=%d)", code)
	}
	assertNoTokenOnDisk(t, s, home)
}

func TestCapsuleGitCloneWithRealGit(t *testing.T) {
	home := isolatedGit(t)
	s := newGitSim(t)
	helper, _ := helperCommand(t)
	writeSession(t, home, s.api.URL, s.session, "refresh-ok")
	apiOld, cfgOld := apiClient, cfg
	defer func() { apiClient, cfg = apiOld, cfgOld }()
	cfg = &config.Config{Token: s.session}
	apiClient = newClientFor(s.api.URL, s.session)
	wd, _ := os.Getwd()
	work := t.TempDir()
	_ = os.Chdir(work)
	defer os.Chdir(wd)

	// global helper: `capsule git clone ORG/REPO DIR`
	var err error
	capture(t, func() {
		err = run(t, mustCmd(t, "git clone"), []string{"org/repo", "web"}, f2("host", s.git.URL, "helper", helper, "no-cache", "true"))
	})
	if err != nil {
		t.Fatalf("git clone: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "web", "README.md")); !strings.Contains(string(b), "hello org/repo") {
		t.Fatalf("clone content %q", b)
	}
	if v := strings.TrimSpace(mustGit(t, "", "config", "--global", "credential."+s.git.URL+".useHttpPath")); v != "true" {
		t.Fatalf("global useHttpPath: %q", v)
	}
	// a later fetch in the clone keeps working through the helper
	mustGit(t, filepath.Join(work, "web"), "fetch", "origin")

	// --local: helper only in the clone's config, not in the global one
	_ = os.Remove(filepath.Join(home, ".gitconfig"))
	capture(t, func() {
		err = run(t, mustCmd(t, "git clone"), []string{"org/repo", "web2"}, f2("host", s.git.URL, "helper", helper, "no-cache", "true", "local", "true"))
	})
	if err != nil {
		t.Fatalf("git clone --local: %v", err)
	}
	if out, gerr := git(t, "", "config", "--global", "--get-all", "credential."+s.git.URL+".helper"); gerr == nil {
		t.Fatalf("--local must not touch the global config: %q", out)
	}
	local := mustGit(t, filepath.Join(work, "web2"), "config", "--local", "--get-all", "credential."+s.git.URL+".helper")
	if !strings.Contains(local, "git credential") {
		t.Fatalf("local helper config missing: %q", local)
	}
	mustGit(t, filepath.Join(work, "web2"), "fetch", "origin")
	assertNoTokenOnDisk(t, s, home, work)

	// wrong repo reference is a usage error and starts no git process
	capture(t, func() { err = run(t, mustCmd(t, "git clone"), []string{"not-a-repo"}, f2("host", s.git.URL)) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("bad repo ref: %v", err)
	}
	// not signed in: exit 3
	cfg = &config.Config{}
	capture(t, func() { err = run(t, mustCmd(t, "git clone"), []string{"org/repo"}, f2("host", s.git.URL)) })
	if exitCodeFor(err) != exitAuth {
		t.Fatalf("no session: %v", err)
	}
	// a service token in the environment is refused
	t.Setenv("CAPSULE_TOKEN", "cgt_service")
	capture(t, func() { err = run(t, mustCmd(t, "git clone"), []string{"org/repo"}, f2("host", s.git.URL)) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("service token: %v", err)
	}
}

func newClientFor(u, tok string) *client.Client { return client.New(u, tok) }

var (
	binOnce sync.Once
	binPath string
	binErr  error
	binDir  string
)

// builtBinary compiles the native capsule binary once per test run.
func builtBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		binDir, binErr = os.MkdirTemp("", "capsule-bin-")
		if binErr != nil {
			return
		}
		binPath = filepath.Join(binDir, "capsule")
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}
		if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
			binErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if binErr != nil {
		t.Fatalf("building the capsule binary: %v", binErr)
	}
	return binPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// TestWrapperGitCloneEndToEnd drives `capsule git login|clone` as a
// subprocess. With the default (native) helper it exercises the binary; with
// CAPSULE_HELPER_CMD="node .../npm/bin/capsule.js" it covers the WASM build,
// the git queue and the Node wrapper that runs it.
func TestWrapperGitCloneEndToEnd(t *testing.T) {
	home := isolatedGit(t)
	s := newGitSim(t)
	helper, bin := helperCommand(t)
	writeSession(t, home, s.api.URL, s.session, "refresh-ok")
	work := t.TempDir()
	cli := func(args ...string) (string, int) {
		c := helperExec(bin, args...)
		c.Dir = work
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		c.Env = append(os.Environ(), "CAPSULE_API_URL="+s.api.URL)
		code := 0
		if err := c.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		return out.String(), code
	}
	if out, code := cli("git", "login", "--host", s.git.URL, "--helper", helper, "--no-cache"); code != 0 {
		t.Fatalf("git login: %d\n%s", code, out)
	}
	if got := mustGit(t, "", "config", "--global", "credential."+s.git.URL+".useHttpPath"); strings.TrimSpace(got) != "true" {
		t.Fatalf("helper config not written: %q", got)
	}
	if out, code := cli("git", "clone", "org/repo", "web", "--host", s.git.URL, "--helper", helper, "--no-cache"); code != 0 {
		t.Fatalf("git clone: %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "web", "README.md")); !strings.Contains(string(b), "hello org/repo") {
		t.Fatalf("clone content %q", b)
	}
	// a git failure becomes the exit code
	if out, code := cli("git", "clone", "secret/repo", "nope", "--host", s.git.URL, "--helper", helper, "--no-cache"); code == 0 {
		t.Fatalf("cloning a hidden repository must fail:\n%s", out)
	}
	// exit codes of the CLI itself
	if _, code := cli("git", "clone", "not-a-repo"); code != exitUsage {
		t.Fatalf("usage error must be exit 2, got %d", code)
	}
	if _, code := cli("pr", "view", "x"); code != exitUsage {
		t.Fatalf("pr view x must be exit 2, got %d", code)
	}
	assertNoTokenOnDisk(t, s, home, work)
}

// helperExec builds the command for the helper. A node-based helper
// (CAPSULE_HELPER_CMD) is started through `sh -c`, exactly as Git starts
// "!" helpers: on Windows a node.exe started directly by a Go test process
// fails every HTTP request from WASM (the plain `capsule whoami` of the
// published 0.6.0 does too), while the same node started by sh works.
func helperExec(bin []string, args ...string) *exec.Cmd {
	if h := os.Getenv("CAPSULE_HELPER_CMD"); h != "" {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'''`) + "'"
		}
		return exec.Command("sh", "-c", h+" "+strings.Join(quoted, " "))
	}
	return exec.Command(bin[0], append(append([]string{}, bin[1:]...), args...)...)
}
