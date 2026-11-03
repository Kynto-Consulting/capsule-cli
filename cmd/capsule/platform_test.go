package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

type call struct {
	Method, Path string
	Body         map[string]any
}

type pmock struct {
	mu     sync.Mutex
	calls  []call
	status int // forced status for everything under /api/v1 except /network/my-ip
	ports  []any
}

func (m *pmock) last() call { return m.calls[len(m.calls)-1] }

func (m *pmock) find(method, suffix string) *call {
	for i := range m.calls {
		if m.calls[i].Method == method && strings.HasSuffix(m.calls[i].Path, suffix) {
			return &m.calls[i]
		}
	}
	return nil
}

func newPMock(t *testing.T) (*pmock, func()) {
	m := &pmock{}
	base := "/api/v1/orgs/" + tOrg + "/projects/" + tProj
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		p := r.URL.Path
		if r.URL.RawQuery != "" {
			p += "?" + r.URL.RawQuery
		}
		m.calls = append(m.calls, call{r.Method, p, body})
		if m.status != 0 && !strings.HasSuffix(p, "/network/my-ip") {
			w.WriteHeader(m.status)
			_, _ = w.Write([]byte(`{"error":{"code":"X","message":"nope"}}`))
			return
		}
		is := func(method, path string) bool { return r.Method == method && r.URL.Path == path }
		switch {
		case p == "/api/v1/network/my-ip":
			_, _ = w.Write([]byte(`{"ip":"198.51.100.9"}`))
		case is("POST", "/api/v1/oauth/clients"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"aaaaaaaa-0000-0000-0000-000000000001","client_id":"cid_123","client_secret":"SUPER-SECRET-VALUE","name":"My app"}`))
		case is("GET", "/api/v1/oauth/clients"):
			_, _ = w.Write([]byte(`{"data":[{"id":"aaaaaaaa-0000-0000-0000-000000000001","client_id":"cid_123","client_secret":"LEAK","name":"My app","redirect_uris":["https://a.example/cb"]}]}`))
		case r.Method == "PATCH" && strings.HasPrefix(p, "/api/v1/oauth/clients/"), r.Method == "DELETE" && strings.HasPrefix(p, "/api/v1/oauth/clients/"):
			w.WriteHeader(204)
		case is("GET", "/api/v1/orgs/"+tOrg+"/members"):
			_, _ = w.Write([]byte(`[{"user_id":"user-1","email":"Ana@Example.com"}]`))
		case is("GET", base+"/members"):
			_, _ = w.Write([]byte(`{"data":[{"user_id":"user-1","email":"ana@example.com","role":"deployer","origin":"project"}]}`))
		case r.Method == "PUT" && strings.HasPrefix(p, base+"/members/"), r.Method == "DELETE" && strings.HasPrefix(p, base+"/members/"):
			w.WriteHeader(204)
		case is("PATCH", "/api/v1/orgs/"+tOrg):
			_, _ = w.Write([]byte(`{}`))
		case is("PUT", base+"/git"):
			_, _ = w.Write([]byte(`{"provider":"forgejo","repo_url":"https://git.example.com/a/b","branch":"main","auth_kind":"token","webhook_url":"https://api/hook"}`))
		case is("GET", base+"/git"):
			_, _ = w.Write([]byte(`{"provider":"forgejo","repo_url":"https://git.example.com/a/b","branch":"main","auth_kind":"token","clone_url_https":"https://git.example.com/a/b.git","clone_url_ssh":"git@git.example.com:a/b.git","token":"MUSTNOTSHOW"}`))
		case is("DELETE", base+"/git"):
			w.WriteHeader(204)
		case is("POST", base+"/git/test"):
			_, _ = w.Write([]byte(`{"ok":true,"message":"reachable"}`))
		case is("POST", base+"/git/repo"):
			_, _ = w.Write([]byte(`{"clone_url_https":"https://git.example.com/a/b.git"}`))
		case is("GET", base+"/git/branches"):
			_, _ = w.Write([]byte(`[{"name":"main","sha":"abcdef1234567"}]`))
		case is("GET", base+"/git/commits"):
			_, _ = w.Write([]byte(`{"data":[{"sha":"abcdef1234567","author":"ana","message":"fix\nbody"}]}`))
		case is("GET", base+"/git/pulls"):
			_, _ = w.Write([]byte(`[{"number":7,"title":"T","state":"open"}]`))
		case is("GET", base+"/previews"):
			_, _ = w.Write([]byte(`[{"pr":7,"status":"running","url":"https://p7.example"}]`))
		case is("POST", base+"/previews"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"url":"https://p7.example"}`))
		case r.Method == "DELETE" && strings.HasPrefix(p, base+"/previews/"):
			w.WriteHeader(204)
		case is("GET", base+"/ports"):
			_ = json.NewEncoder(w).Encode(map[string]any{"address_host": "ports.example", "ports": m.ports})
		case is("PUT", base+"/ports"):
			m.ports, _ = body["ports"].([]any)
			for _, it := range m.ports {
				pm := it.(map[string]any)
				pm["host_port"] = 41000
				if cs, ok := pm["cidrs"].([]any); ok {
					for i, c := range cs {
						c.(map[string]any)["id"] = "rule" + string(rune('0'+i)) + "0000000"
					}
				}
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "POST" && strings.Contains(p, "/ports/") && strings.HasSuffix(p, "/access"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "DELETE" && strings.Contains(p, "/ports/") && strings.Contains(p, "/access/"):
			w.WriteHeader(204)
		case is("GET", base+"/backups"):
			_, _ = w.Write([]byte(`{"enabled":true,"schedule":"daily","retention_days":7,"include":{"databases":true,"volumes":true},"next_run":"2026-11-03T00:00:00Z","last_runs":[{"started_at":"t","status":"ok","snapshot_id":"snap1234-aaaa"}]}`))
		case is("PUT", base+"/backups"), is("POST", base+"/backups/run"), is("POST", base+"/backups/restore"):
			_, _ = w.Write([]byte(`{"snapshot_id":"snap1234-aaaa"}`))
		case is("GET", base+"/backups/snapshots"):
			_, _ = w.Write([]byte(`[{"id":"snap1234-aaaa-bbbb","created_at":"t","size_bytes":10}]`))
		case is("GET", "/api/v1/platform/capabilities"):
			_, _ = w.Write([]byte(`{"backups":{"enabled":true,"default_schedule":"daily"},"git":{"enabled":false},"ports":{"enabled":true,"range":"41000-41999","host":"ports.example"},"oidc":{"enabled":true,"issuer":"https://id.example"}}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"NF","message":"` + r.Method + " " + p + `"}}`))
		}
	}))
	old := apiClient
	apiClient = client.New(srv.URL, "tok")
	t.Setenv("CAPSULE_ORG_ID", tOrg)
	t.Setenv("CAPSULE_PROJECT_ID", tProj)
	return m, func() { srv.Close(); apiClient = old }
}

// capture runs fn and returns what it wrote to stdout.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func jsonOutput(t *testing.T, c *cobra.Command) string {
	t.Helper()
	_ = rootCmd.PersistentFlags().Set("output", "json")
	defer rootCmd.PersistentFlags().Set("output", "table")
	out, _ := runOut(t, c, nil, nil)
	return out
}

func runOut(t *testing.T, c *cobra.Command, args []string, flags map[string][]string) (string, error) {
	t.Helper()
	var err error
	out := capture(t, func() { err = run(t, c, args, flags) })
	return out, err
}

func TestOAuthSecretShownOnce(t *testing.T) {
	m, done := newPMock(t)
	defer done()
	out, err := runOut(t, oauthCreateCmd, nil, map[string][]string{"name": {"My app"}, "redirect-uri": {"https://a.example/cb"}, "first-party": {"true"}})
	if err != nil || strings.Count(out, "SUPER-SECRET-VALUE") != 1 || !strings.Contains(out, "ONCE") {
		t.Fatalf("%v\n%s", err, out)
	}
	if b := m.last().Body; b["first_party"] != true || b["name"] != "My app" {
		t.Fatalf("%v", b)
	}
	out, err = runOut(t, oauthListCmd, nil, nil)
	if err != nil || strings.Contains(out, "SUPER-SECRET") || strings.Contains(out, "LEAK") || !strings.Contains(out, "cid_123") {
		t.Fatalf("%v\n%s", err, out)
	}
	out = jsonOutput(t, oauthListCmd)
	if strings.Contains(out, "LEAK") {
		t.Fatal("secret in json list")
	}
	if err := run(t, oauthCreateCmd, nil, map[string][]string{"name": {"x"}, "redirect-uri": {"http://evil.example/cb"}}); err == nil {
		t.Fatal("plain http redirect must be rejected")
	}
	if err := run(t, oauthUpdateCmd, []string{"cid_123"}, map[string][]string{"name": {"New"}}); err != nil {
		t.Fatal(err)
	}
	if c := m.last(); c.Method != "PATCH" || !strings.HasSuffix(c.Path, "/oauth/clients/aaaaaaaa-0000-0000-0000-000000000001") {
		t.Fatalf("%+v", c)
	}
	if err := run(t, oauthDeleteCmd, []string{"aaaaaaaa"}, map[string][]string{"yes": {"true"}}); err != nil {
		t.Fatal(err)
	}
	if m.last().Method != "DELETE" {
		t.Fatalf("%+v", m.last())
	}
}

func TestMembersAndAccessMode(t *testing.T) {
	m, done := newPMock(t)
	defer done()
	if _, err := runOut(t, membersListCmd, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := run(t, membersSetCmd, []string{"ana@example.com"}, map[string][]string{"role": {"deployer"}}); err != nil {
		t.Fatal(err)
	}
	if c := m.last(); c.Method != "PUT" || !strings.HasSuffix(c.Path, "/members/user-1") || c.Body["role"] != "deployer" {
		t.Fatalf("%+v", c)
	}
	if err := run(t, membersSetCmd, []string{"u"}, map[string][]string{"role": {"owner"}}); err == nil {
		t.Fatal("invalid role")
	}
	if err := run(t, membersRemoveCmd, []string{"user-9"}, map[string][]string{"yes": {"true"}}); err != nil || m.last().Method != "DELETE" {
		t.Fatal(err)
	}
	if err := run(t, membersSetCmd, []string{"nobody@example.com"}, map[string][]string{"role": {"viewer"}}); err == nil {
		t.Fatal("unknown email")
	}
	if err := run(t, orgAccessModeCmd, []string{"restricted"}, map[string][]string{"yes": {"true"}}); err != nil {
		t.Fatal(err)
	}
	if c := m.last(); c.Method != "PATCH" || c.Body["project_access_mode"] != "restricted" {
		t.Fatalf("%+v", c)
	}
	n := len(m.calls)
	if err := run(t, orgAccessModeCmd, []string{"restricted"}, nil); err == nil || len(m.calls) != n {
		t.Fatal("restricted must not apply without confirmation")
	}
	if err := run(t, orgAccessModeCmd, []string{"weird"}, nil); err == nil {
		t.Fatal("invalid mode")
	}
}

func withStdin(t *testing.T, content string) func() {
	t.Helper()
	f := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(f, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	fh, _ := os.Open(f)
	old := os.Stdin
	os.Stdin = fh
	return func() { os.Stdin = old; fh.Close() }
}

func TestGitFlowsAndTokenNeverPrinted(t *testing.T) {
	m, done := newPMock(t)
	defer done()
	restore := withStdin(t, "ghp_TOPSECRETTOKEN\n")
	out, err := runOut(t, gitLinkCmd, nil, map[string][]string{"provider": {"forgejo"}, "repo-url": {"https://git.example.com/a/b"}, "token-stdin": {"true"}, "previews": {"true"}})
	restore()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "TOPSECRET") || strings.Contains(out, "token-stdin") {
		t.Fatalf("output leaks: %s", out)
	}
	c := m.find("PUT", "/git")
	if c.Body["token"] != "ghp_TOPSECRETTOKEN" || c.Body["auth_kind"] != "token" || c.Body["preview_enabled"] != true || c.Body["provider"] != "forgejo" {
		t.Fatalf("%v", c.Body)
	}
	// token auth without stdin is refused locally
	n := len(m.calls)
	if err := run(t, gitLinkCmd, nil, map[string][]string{"repo-url": {"https://x/y"}, "auth": {"token"}}); err == nil || len(m.calls) != n {
		t.Fatal("auth token without --token-stdin must fail locally")
	}
	if err := run(t, gitLinkCmd, nil, map[string][]string{"repo-url": {"https://x/y"}, "provider": {"svn"}}); err == nil {
		t.Fatal("bad provider")
	}
	out, err = runOut(t, gitShowCmd, nil, nil)
	if err != nil || strings.Contains(out, "MUSTNOTSHOW") {
		t.Fatalf("%v %s", err, out)
	}
	out = jsonOutput(t, gitShowCmd)
	if strings.Contains(out, "MUSTNOTSHOW") {
		t.Fatal("token in json")
	}
	out, err = runOut(t, gitRemoteCmd, nil, nil)
	if err != nil || !strings.Contains(out, "git remote add capsule https://git.example.com/a/b.git") {
		t.Fatalf("%v %s", err, out)
	}
	out, _ = runOut(t, gitRemoteCmd, nil, map[string][]string{"ssh": {"true"}})
	if !strings.Contains(out, "git@git.example.com:a/b.git") {
		t.Fatal(out)
	}
	for _, cmd := range []*cobra.Command{gitTestCmd, gitRepoCmd, gitBranchesCmd, gitCommitsCmd, gitPullsCmd} {
		if _, err := runOut(t, cmd, nil, nil); err != nil {
			t.Fatalf("%s: %v", cmd.Use, err)
		}
	}
	if err := run(t, gitRepoCmd, nil, map[string][]string{"import-url": {"https://github.com/o/r"}}); err != nil || m.last().Body["import_url"] != "https://github.com/o/r" {
		t.Fatal(err)
	}
	if err := run(t, gitPullsCmd, nil, map[string][]string{"state": {"all"}}); err != nil || !strings.HasSuffix(m.last().Path, "pulls?state=all") {
		t.Fatalf("%v %s", err, m.last().Path)
	}
	if err := run(t, gitCommitsCmd, nil, map[string][]string{"branch": {"dev"}, "page": {"2"}}); err != nil || !strings.HasSuffix(m.last().Path, "commits?branch=dev&page=2") {
		t.Fatalf("%v %s", err, m.last().Path)
	}
	if err := run(t, gitUnlinkCmd, nil, map[string][]string{"yes": {"true"}}); err != nil || m.last().Method != "DELETE" {
		t.Fatal(err)
	}
}

func TestPreviews(t *testing.T) {
	m, done := newPMock(t)
	defer done()
	if _, err := runOut(t, previewsListCmd, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := run(t, previewsCreateCmd, nil, map[string][]string{"pr": {"7"}, "ref": {"feat"}, "sha": {"abc"}}); err != nil {
		t.Fatal(err)
	}
	if b := m.last().Body; b["pr"] != float64(7) || b["sha"] != "abc" || b["ref"] != "feat" {
		t.Fatalf("%v", b)
	}
	if err := run(t, previewsCreateCmd, nil, nil); err == nil {
		t.Fatal("--pr required")
	}
	if err := run(t, previewsDeleteCmd, []string{"#7"}, map[string][]string{"yes": {"true"}}); err != nil || !strings.HasSuffix(m.last().Path, "/previews/7") {
		t.Fatal(err, m.last())
	}
	if err := run(t, previewsDeleteCmd, []string{"x"}, nil); err == nil {
		t.Fatal("non numeric")
	}
}

func TestPortsFlow(t *testing.T) {
	m, done := newPMock(t)
	defer done()
	// default private
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"9000"}}); err != nil {
		t.Fatal(err)
	}
	p := m.ports[0].(map[string]any)
	if p["access"] != "private" || p["protocol"] != "tcp" || p["name"] != "port-9000" {
		t.Fatalf("%v", p)
	}
	// --my-ip and normalized cidr, existing port preserved
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"8080"}, "name": {"web"}, "my-ip": {"true"}, "cidr": {"10.0.0.5/24"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.ports) != 2 {
		t.Fatalf("%v", m.ports)
	}
	q := m.ports[1].(map[string]any)
	cs := q["cidrs"].([]any)
	if q["access"] != "allowlist" || len(cs) != 2 {
		t.Fatalf("%v", q)
	}
	got := []string{cs[0].(map[string]any)["cidr"].(string), cs[1].(map[string]any)["cidr"].(string)}
	if got[0] != "10.0.0.0/24" || got[1] != "198.51.100.9/32" {
		t.Fatalf("%v", got)
	}
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"8080"}}); err == nil {
		t.Fatal("duplicate port")
	}
	// public needs --allow-public and confirmation
	n := len(m.calls)
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"7000"}, "cidr": {"0.0.0.0/0"}}); err == nil || len(m.calls) != n {
		t.Fatal("public without --allow-public must fail locally")
	}
	restore := withStdin(t, "")
	err := run(t, portsAddCmd, nil, map[string][]string{"container": {"7000"}, "cidr": {"0.0.0.0/0"}, "allow-public": {"true"}})
	restore()
	if err == nil {
		t.Fatal("public without --yes must abort")
	}
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"7000"}, "cidr": {"0.0.0.0/0"}, "allow-public": {"true"}, "yes": {"true"}}); err != nil {
		t.Fatal(err)
	}
	if m.ports[2].(map[string]any)["access"] != "public" {
		t.Fatalf("%v", m.ports[2])
	}
	out, err := runOut(t, portsListCmd, nil, nil)
	if err != nil || !strings.Contains(out, "ports.example:41000") {
		t.Fatalf("%v %s", err, out)
	}
	// access add / remove
	if err := run(t, portsAccessAddCmd, []string{"9000"}, map[string][]string{"cidr": {"203.0.113.4"}, "desc": {"office"}}); err != nil {
		t.Fatal(err)
	}
	if c := m.last(); !strings.HasSuffix(c.Path, "/ports/9000/access") || c.Body["cidr"] != "203.0.113.4/32" || c.Body["description"] != "office" {
		t.Fatalf("%+v", c)
	}
	if err := run(t, portsAccessAddCmd, []string{"9000"}, nil); err == nil {
		t.Fatal("needs cidr")
	}
	if err := run(t, portsAccessRemoveCmd, []string{"8080", "10.0.0.0/24"}, nil); err != nil {
		t.Fatal(err)
	}
	if c := m.last(); c.Method != "DELETE" || !strings.HasSuffix(c.Path, "/ports/8080/access/rule00000000") {
		t.Fatalf("%+v", c)
	}
	if err := run(t, portsAccessRemoveCmd, []string{"8080", "rule1"}, nil); err != nil {
		t.Fatal(err)
	}
	// remove port
	if err := run(t, portsRemoveCmd, nil, map[string][]string{"container": {"9000"}, "yes": {"true"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.ports) != 2 {
		t.Fatalf("%v", m.ports)
	}
	if err := run(t, portsRemoveCmd, nil, map[string][]string{"container": {"1"}, "yes": {"true"}}); err == nil {
		t.Fatal("unknown port")
	}
}

func TestBackups(t *testing.T) {
	m, done := newPMock(t)
	defer done()
	out, err := runOut(t, backupsShowCmd, nil, nil)
	if err != nil || !strings.Contains(out, "daily") || !strings.Contains(out, "snap1234") {
		t.Fatalf("%v %s", err, out)
	}
	if err := run(t, backupsSetCmd, nil, map[string][]string{"schedule": {"6h"}, "retention-days": {"14"}, "volumes": {"false"}, "enabled": {"false"}}); err != nil {
		t.Fatal(err)
	}
	b := m.last().Body
	inc := b["include"].(map[string]any)
	if b["schedule"] != "6h" || b["retention_days"] != float64(14) || b["enabled"] != false || inc["volumes"] != false || inc["databases"] != true {
		t.Fatalf("%v", b)
	}
	if err := run(t, backupsSetCmd, nil, map[string][]string{"schedule": {"monthly"}}); err == nil {
		t.Fatal("bad schedule")
	}
	if err := run(t, backupsSetCmd, nil, nil); err == nil {
		t.Fatal("nothing to change")
	}
	if _, err := runOut(t, backupsListCmd, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := run(t, backupsRunCmd, nil, nil); err != nil {
		t.Fatal(err)
	}
	n := len(m.calls)
	restore := withStdin(t, "")
	err = run(t, backupsRestoreCmd, []string{"snap1234"}, nil)
	restore()
	if err == nil {
		t.Fatal("restore must need confirmation")
	}
	for _, c := range m.calls[n:] {
		if strings.HasSuffix(c.Path, "/restore") {
			t.Fatal("restore sent without confirmation")
		}
	}
	if err := run(t, backupsRestoreCmd, []string{"snap1234"}, map[string][]string{"yes": {"true"}, "target": {"staging"}}); err != nil {
		t.Fatal(err)
	}
	if b := m.last().Body; b["snapshot_id"] != "snap1234-aaaa-bbbb" || b["target"] != "staging" {
		t.Fatalf("%v", b)
	}
}

func TestPlatformAndErrors(t *testing.T) {
	m, done := newPMock(t)
	defer done()
	out, err := runOut(t, platformCmd, nil, nil)
	if err != nil || !strings.Contains(out, "git") || !strings.Contains(out, "disabled") || !strings.Contains(out, "41000-41999") {
		t.Fatalf("%v %s", err, out)
	}
	cases := []struct {
		status int
		want   string
	}{{403, "Not allowed"}, {404, "does not support"}, {501, "not enabled"}}
	for _, tc := range cases {
		m.status = tc.status
		for name, fn := range map[string]func() error{
			"backups": func() error { return run(t, backupsShowCmd, nil, nil) },
			"ports":   func() error { return run(t, portsListCmd, nil, nil) },
			"git":     func() error { return run(t, gitShowCmd, nil, nil) },
			"oauth":   func() error { return run(t, oauthListCmd, nil, nil) },
			"members": func() error { return run(t, membersListCmd, nil, nil) },
			"preview": func() error { return run(t, previewsListCmd, nil, nil) },
			"plat":    func() error { return run(t, platformCmd, nil, nil) },
		} {
			if err := fn(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s %d: %v", name, tc.status, err)
			}
		}
	}
}

func TestForgejoWorkflows(t *testing.T) {
	d := filepath.ToSlash(t.TempDir())
	write(t, d, "package.json", `{"scripts":{"lint":"x","test":"y"}}`)
	st := detectCIStack(d, "", "auto")
	f := generateForgejo(st, ciOptions{Branch: "main"})
	for name, y := range map[string]string{"ci": f.CI, "deploy": f.Deploy} {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(y), &m); err != nil {
			t.Fatalf("%s invalid YAML: %v", name, err)
		}
	}
	var ci map[string]any
	_ = yaml.Unmarshal([]byte(f.CI), &ci)
	jobs := ci["jobs"].(map[string]any)
	for _, j := range []string{"secrets", "quality", "semgrep", "osv", "trivy"} {
		if jobs[j] == nil {
			t.Errorf("missing job %s", j)
		}
	}
	for _, want := range []string{"gitleaks", "semgrep scan", "osv-scanner scan", "trivy fs", "CodeQL and Dependabot", "Renovate", "npm run --if-present lint", "8.21.2"} {
		if !strings.Contains(f.CI, want) {
			t.Errorf("ci.yml missing %q", want)
		}
	}
	for _, want := range []string{"@kyntocg/capsule-cli@0.6.0", "secrets.CAPSULE_TOKEN", "secrets.CAPSULE_ORG_ID", "secrets.CAPSULE_PROJECT_ID", "capsule deploy"} {
		if !strings.Contains(f.Deploy, want) {
			t.Errorf("deploy.yml missing %q", want)
		}
	}
	if strings.Contains(f.CI+f.Deploy, "@latest") || strings.Contains(f.CI, "@main") {
		t.Error("versions must be pinned")
	}
}

func TestForgejoInitWritesFiles(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.ToSlash(d), "go.mod", "module x\n\ngo 1.22\n")
	t.Setenv("CAPSULE_CWD", d)
	if err := run(t, ciInitCmd, nil, map[string][]string{"provider": {"forgejo"}}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".forgejo/workflows/ci.yml", ".forgejo/workflows/deploy.yml"} {
		if _, err := os.Stat(filepath.Join(d, f)); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(t, ciInitCmd, nil, map[string][]string{"provider": {"forgejo"}}); err == nil {
		t.Fatal("must not overwrite without --force")
	}
	if err := run(t, ciInitCmd, nil, map[string][]string{"provider": {"forgejo"}, "set-secrets": {"true"}, "force": {"true"}}); err == nil {
		t.Fatal("--set-secrets unsupported for forgejo")
	}
	if err := run(t, ciInitCmd, nil, map[string][]string{"provider": {"gitlab"}}); err == nil {
		t.Fatal("unsupported provider")
	}
}
