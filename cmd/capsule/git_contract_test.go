package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/kynto-consulting/capsule/cli/internal/client"
	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// Contract tests: the CLI against docs/git-capsule/API.openapi.yaml.
//
//  1. TestEveryOperationHasACommand walks the YAML and fails when an
//     operationId has no command (or a registered command does not exist).
//  2. TestCommandsMatchTheContract runs a command for every operation against
//     an httptest server that only accepts routes, query parameters and body
//     fields declared in the YAML, and checks that every operation was reached.

const specPath = "../../../docs/git-capsule/API.openapi.yaml"

type specRoute struct {
	method, path, opID string
	re                 *regexp.Regexp
	query              map[string]bool
	bodyProps          map[string]bool
	bodyRequired       []string
	idempotent         bool
}

type apiSpec struct {
	routes []*specRoute
	byOp   map[string]*specRoute
}

func loadSpec(t *testing.T) *apiSpec {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}
	comps, _ := doc["components"].(map[string]any)
	resolve := func(m map[string]any) map[string]any {
		for i := 0; i < 10; i++ {
			ref, ok := m["$ref"].(string)
			if !ok {
				return m
			}
			parts := strings.Split(ref, "/")
			sec, _ := comps[parts[len(parts)-2]].(map[string]any)
			next, _ := sec[parts[len(parts)-1]].(map[string]any)
			if next == nil {
				return map[string]any{}
			}
			m = next
		}
		return m
	}
	var props func(m map[string]any, into map[string]bool, req *[]string)
	props = func(m map[string]any, into map[string]bool, req *[]string) {
		m = resolve(m)
		if p, ok := m["properties"].(map[string]any); ok {
			for k := range p {
				into[k] = true
			}
		}
		if r, ok := m["required"].([]any); ok {
			for _, x := range r {
				*req = append(*req, x.(string))
			}
		}
		if all, ok := m["allOf"].([]any); ok {
			for _, x := range all {
				props(x.(map[string]any), into, req)
			}
		}
	}
	spec := &apiSpec{byOp: map[string]*specRoute{}}
	paths, _ := doc["paths"].(map[string]any)
	for p, item := range paths {
		im := item.(map[string]any)
		for method, v := range im {
			o, ok := v.(map[string]any)
			if !ok || method == "parameters" {
				continue
			}
			id, _ := o["operationId"].(string)
			if id == "" {
				continue
			}
			r := &specRoute{method: strings.ToUpper(method), path: p, opID: id, query: map[string]bool{}, bodyProps: map[string]bool{}}
			segs := strings.Split(p, "/")
			for i, sg := range segs {
				if strings.HasPrefix(sg, "{") {
					segs[i] = "[^/]+"
				} else {
					segs[i] = regexp.QuoteMeta(sg)
				}
			}
			r.re = regexp.MustCompile("^/api/v1" + strings.Join(segs, "/") + "$")
			var params []any
			if pl, ok := im["parameters"].([]any); ok {
				params = append(params, pl...)
			}
			if pl, ok := o["parameters"].([]any); ok {
				params = append(params, pl...)
			}
			for _, x := range params {
				pm := resolve(x.(map[string]any))
				if pm["in"] == "query" {
					r.query[pm["name"].(string)] = true
				}
			}
			if rb, ok := o["requestBody"].(map[string]any); ok {
				rb = resolve(rb)
				if c, ok := rb["content"].(map[string]any); ok {
					for _, cv := range c {
						if sch, ok := cv.(map[string]any)["schema"].(map[string]any); ok {
							props(sch, r.bodyProps, &r.bodyRequired)
						}
					}
				}
			}
			r.idempotent, _ = o["x-idempotent"].(bool)
			spec.routes = append(spec.routes, r)
			spec.byOp[id] = r
		}
	}
	if len(spec.routes) < 100 {
		t.Fatalf("contract parsed with only %d operations", len(spec.routes))
	}
	return spec
}

func TestEveryOperationHasACommand(t *testing.T) {
	spec := loadSpec(t)
	var ids []string
	for id := range spec.byOp {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := excludedOps[id]; ok {
			continue
		}
		words, ok := opCommands[id]
		if !ok {
			if why, reserved := reservedOps[id]; reserved {
				t.Logf("reserved for C3: %s -> %s (recover.go not written yet)", id, why)
				continue
			}
			t.Errorf("operationId %s has no capsule command: add it to opCommands (git_opregistry.go)", id)
			continue
		}
		c, rest, err := rootCmd.Find(strings.Fields(words))
		if err != nil || c == rootCmd || len(rest) > 0 || !c.Runnable() {
			t.Errorf("operationId %s maps to %q, which is not a runnable command (found %v rest %v err %v)", id, words, c.CommandPath(), rest, err)
		}
	}
	for id := range opCommands {
		if _, ok := spec.byOp[id]; !ok {
			t.Errorf("opCommands lists %s, which is not in the contract", id)
		}
	}
}

// ── contract harness ─────────────────────────────────────────────────────────

const (
	uuidA = "11111111-1111-4111-8111-111111111111"
	uuidB = "22222222-2222-4222-8222-222222222222"
)

type contractServer struct {
	t        *testing.T
	spec     *apiSpec
	mu       sync.Mutex
	seen     map[string]bool // all ops, whole run
	caseSeen map[string]bool
	bodies   map[string]map[string]any
	// existing commands that predate the contract: parameters/headers they send
	// that the YAML does not declare (reported, not hidden).
	knownQuery map[string][]string
	noIdem     map[string]bool
}

var contractFixtures = map[string]string{
	"pullGet":            `{"number":12,"title":"T","state":"open","head":"f","base":"main","html_url":"https://git.example.com/a/b/pulls/12","mergeable":{"can_merge":false,"blockers":[{"code":"POLICY_BLOCKED","message":"1 high alert"}]}}`,
	"repoGet":            `{"default_branch":"main","provision_status":"ready","clone_https":"https://git.example.com/a/b.git"}`,
	"repoBlob":           `{"path":"README.md","encoding":"utf-8","content":"hello\n"}`,
	"protectionsList":    `{"items":[{"id":"` + uuidA + `","pattern":"main","require_pull_request":true,"forgejo_synced":true}]}`,
	"protectionPut":      `{"id":"` + uuidA + `","pattern":"main","forgejo_synced":true}`,
	"runGet":             `{"id":"77","status":"success","workflow":"CI","jobs":[{"id":"j1","name":"test","status":"success"}]}`,
	"workflowsList":      `{"items":[{"id":"ci.yml","name":"CI","path":".forgejo/workflows/ci.yml"}]}`,
	"jobLogs":            `{"lines":["step 1"],"complete":true}`,
	"securityPolicyGet":  `{"block_merge_on":{"dependency":"high","code":"high","secret":"any"},"block_deploy_on":"high"}`,
	"envGet":             `{"id":"` + uuidA + `","name":"production","kind":"production","reviewers":[]}`,
	"approvalsPending":   `{"items":[{"id":"` + uuidA + `","environment":"production","sha":"abcdef1","project_id":"` + tProj + `"}]}`,
	"gitCredentialIssue": `{"username":"ana","password":"cgt_TESTVALUE","expires_at":"2099-01-01T00:00:00Z"}`,
	"gitTokenCreate":     `{"id":"` + uuidA + `","name":"ci","token":"cgt_NEWTOKEN","expires_at":"2099-01-01T00:00:00Z"}`,
	"serviceTokenCreate": `{"id":"` + uuidA + `","name":"ci","token":"cgt_SVC","expires_at":"2099-01-01T00:00:00Z"}`,
	"webhooksList":       `{"items":[{"id":"` + uuidA + `","kind":"slack","url":"https://hooks.example.com/x","events":["pr.merged"],"active":true}]}`,
	"webhookCreate":      `{"id":"` + uuidA + `","secret":"whsec_ONCE"}`,
	"promotionCreate":    `{"id":"` + uuidA + `","environment":"production","status":"approved"}`,
	"gitLinkGet":         `{"provider":"forgejo","repo_url":"https://git.example.com/a/b"}`,
	"gitLinkPut":         `{"provider":"forgejo","repo_url":"https://git.example.com/a/b"}`,
	"gitLinkTest":        `{"ok":true,"message":"reachable"}`,
	"membersList":        `[{"user_id":"user-1","email":"ana@example.com","role":"deployer"}]`,
}

func (s *contractServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v1/auth/me":
		_, _ = w.Write([]byte(`{"login":"ana","email":"ana@example.com"}`))
		return
	case r.Method == "GET" && r.URL.Path == "/api/v1/platform/capabilities":
		_, _ = w.Write([]byte(`{"git":{"enabled":true,"url":"https://git.example.com","ssh_host":"git.example.com"}}`))
		return
	}
	var route *specRoute
	for _, rt := range s.spec.routes {
		if rt.method == r.Method && rt.re.MatchString(r.URL.EscapedPath()) {
			route = rt
			break
		}
	}
	if route == nil {
		s.t.Errorf("request to a route that is not in the contract: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"not in contract"}}`))
		return
	}
	s.seen[route.opID] = true
	s.caseSeen[route.opID] = true
	for k := range r.URL.Query() {
		if route.query[k] {
			continue
		}
		known := false
		for _, kk := range s.knownQuery[route.opID] {
			known = known || kk == k
		}
		if !known {
			s.t.Errorf("%s: query parameter %q is not in the contract (declared: %v)", route.opID, k, keysOf(route.query))
		}
	}
	if route.idempotent && r.Header.Get("Idempotency-Key") == "" && !s.noIdem[route.opID] {
		s.t.Errorf("%s: x-idempotent operation sent without Idempotency-Key", route.opID)
	}
	b, _ := io.ReadAll(r.Body)
	if len(b) > 0 {
		var body map[string]any
		if err := json.Unmarshal(b, &body); err == nil {
			s.bodies[route.opID] = body
			for k := range body {
				if len(route.bodyProps) > 0 && !route.bodyProps[k] {
					s.t.Errorf("%s: body field %q is not in the contract (declared: %v)", route.opID, k, keysOf(route.bodyProps))
				}
			}
			for _, req := range route.bodyRequired {
				if _, ok := body[req]; !ok {
					s.t.Errorf("%s: required body field %q missing", route.opID, req)
				}
			}
		}
	}
	if fx, ok := contractFixtures[route.opID]; ok {
		_, _ = w.Write([]byte(fx))
		return
	}
	switch r.Method {
	case "DELETE":
		w.WriteHeader(204)
	case "GET":
		_, _ = w.Write([]byte(`{"items":[],"next_cursor":null}`))
	default:
		_, _ = w.Write([]byte(`{"id":"` + uuidA + `"}`))
	}
}

func keysOf(m map[string]bool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

type contractCase struct {
	op    string
	words string
	args  []string
	flags map[string][]string
	stdin string
}

func f(kv ...string) map[string][]string {
	m := map[string][]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = append(m[kv[i]], kv[i+1])
	}
	return m
}

func contractCases() []contractCase {
	yes := []string{"yes", "true"}
	return []contractCase{
		{op: "gitStatus", words: "git status"},
		{op: "gitReconcile", words: "git reconcile", flags: f(yes...)},
		{op: "gitRecoverPlatform", words: "recover plan", flags: f("all", "true")}, // C3
		{op: "recoverProject", words: "recover apply", flags: f(yes...)},          // C3
		{op: "gitRecoverGet", words: "recover status", args: []string{uuidA}},     // C3
		{op: "gitLinkGet", words: "git show"},
		{op: "gitLinkPut", words: "git link", flags: f("repo-url", "https://git.example.com/a/b", "provider", "forgejo")},
		{op: "gitLinkDelete", words: "git unlink", flags: f(yes...)},
		{op: "gitLinkTest", words: "git test"},
		{op: "repoGet", words: "git repo view"},
		{op: "repoProvision", words: "git repo create"},
		{op: "repoUpdate", words: "git repo settings", flags: f("default-branch", "main")},
		{op: "repoTree", words: "git tree", args: []string{"src"}, flags: f("ref", "main")},
		{op: "repoBlob", words: "git cat", args: []string{"README.md"}},
		{op: "repoCompare", words: "git compare", args: []string{"main", "fix"}},
		{op: "branchesList", words: "branch list"},
		{op: "branchCreate", words: "branch create", args: []string{"fix/x"}, flags: f("from", "main")},
		{op: "branchDelete", words: "branch delete", args: []string{"fix/x"}, flags: f(yes...)},
		{op: "tagsList", words: "git tags list"},
		{op: "tagCreate", words: "git tags create", args: []string{"v1"}, flags: f("target", "main")},
		{op: "tagDelete", words: "git tags delete", args: []string{"v1"}, flags: f(yes...)},
		{op: "commitsList", words: "git commits"},
		{op: "commitGet", words: "git commits show", args: []string{"abc1234"}},
		{op: "commitChecks", words: "git commits checks", args: []string{"abc1234"}},
		{op: "activityList", words: "git activity"},
		{op: "insights", words: "git insights", flags: f("since", "30d")},
		{op: "protectionsList", words: "git protect list"},
		{op: "protectionCreate", words: "git protect set", args: []string{"release/**"}, flags: f("require-pr", "true", "approvals", "1", "checks", "ci / test", "no-force-push", "true")},
		{op: "protectionPut", words: "git protect set", args: []string{"main"}, flags: f("approvals", "2")},
		{op: "protectionDelete", words: "git protect remove", args: []string{"main"}, flags: f(yes...)},
		{op: "protectionEvaluate", words: "git protect evaluate", flags: f("branch", "main")},
		{op: "codeownersGet", words: "git codeowners"},
		{op: "mirrorGet", words: "git mirror show"},
		{op: "mirrorPut", words: "git mirror set", flags: f("remote-url", "https://other.example.com/a/b.git", "interval", "daily")},
		{op: "mirrorDelete", words: "git mirror remove", flags: f(yes...)},
		{op: "mirrorSync", words: "git mirror sync"},
		{op: "importCreate", words: "git import github", flags: f("repos", "a/b", "token-stdin", "true"), stdin: "ghp_x\n"},
		{op: "importGet", words: "git import status", args: []string{uuidA}},
		{op: "gitTokensList", words: "git tokens list"},
		{op: "gitTokenCreate", words: "git tokens create", flags: f("name", "ci", "scope", "read", "expires", "30d")},
		{op: "gitTokenRevoke", words: "git tokens revoke", args: []string{uuidA}, flags: f(yes...)},
		{op: "gitSshKeysList", words: "git ssh-keys list"},
		{op: "gitSshKeyAdd", words: "git ssh-keys add", flags: f("title", "laptop", "key", "ssh-ed25519 AAAAC3Nza test")},
		{op: "gitSshKeyDelete", words: "git ssh-keys remove", args: []string{uuidA}, flags: f(yes...)},
		{op: "serviceTokensList", words: "git service-tokens list"},
		{op: "serviceTokenCreate", words: "git service-tokens create", flags: f("name", "ci", "scope", "read", "expires", "90d")},
		{op: "serviceTokenRevoke", words: "git service-tokens revoke", args: []string{uuidA}, flags: f(yes...)},
		{op: "gitCredentialIssue", words: "git credential get", stdin: "protocol=https\nhost=git.example.com\npath=org/repo.git\n\n"},
		{op: "pullsList", words: "pr list", flags: f("state", "open", "author", "@me", "base", "main")},
		{op: "pullCreate", words: "pr create", flags: f("title", "T", "head", "f", "base", "main", "draft", "true", "reviewer", "ana")},
		{op: "pullGet", words: "pr view", args: []string{"12"}},
		{op: "commentsList", words: "pr view", args: []string{"#12"}, flags: f("comments", "true")},
		{op: "reviewsList", words: "pr view", args: []string{"12"}},
		{op: "pullUpdate", words: "pr edit", args: []string{"12"}, flags: f("title", "N")},
		{op: "pullReady", words: "pr ready", args: []string{"12"}},
		{op: "pullFiles", words: "pr files", args: []string{"12"}},
		{op: "pullCommits", words: "pr commits", args: []string{"12"}},
		{op: "pullChecks", words: "pr checks", args: []string{"12"}},
		{op: "pullUpdateBranch", words: "pr update-branch", args: []string{"12"}},
		{op: "pullMerge", words: "pr merge", args: []string{"12"}, flags: f("squash", "true", "head-sha", "abc")},
		{op: "autoMergeEnable", words: "pr merge", args: []string{"12"}, flags: f("auto", "true", "squash", "true")},
		{op: "autoMergeDisable", words: "pr merge", args: []string{"12"}, flags: f("disable-auto", "true")},
		{op: "commentCreate", words: "pr comment", args: []string{"12"}, flags: f("body", "x")},
		{op: "reviewCreate", words: "pr review", args: []string{"12"}, flags: f("request-changes", "true", "body", "no", "file", "a.go:3:fix")},
		{op: "reviewDismiss", words: "pr dismiss-review", args: []string{"12", uuidA}, flags: f("reason", "stale")},
		{op: "reviewersRequest", words: "pr request-review", args: []string{"12"}, flags: f("user", "ana", "team", "legal")},
		{op: "workflowsList", words: "workflow list"},
		{op: "workflowDispatch", words: "workflow run", args: []string{"ci.yml"}, flags: f("ref", "main", "field", "env=prod")},
		{op: "runsList", words: "runs list", flags: f("branch", "main", "status", "failure")},
		{op: "runGet", words: "runs view", args: []string{"77"}},
		{op: "jobLogs", words: "runs logs", args: []string{"77"}},
		{op: "runRerun", words: "runs rerun", args: []string{"77"}, flags: f("failed", "true")},
		{op: "runCancel", words: "runs cancel", args: []string{"77"}, flags: f(yes...)},
		{op: "artifactsList", words: "runs artifacts", args: []string{"77"}},
		{op: "ciSecretsList", words: "ci secrets list"},
		{op: "ciSecretPut", words: "ci secrets set", args: []string{"TOKEN_X"}, stdin: "v\n"},
		{op: "ciSecretDelete", words: "ci secrets delete", args: []string{"TOKEN_X"}, flags: f(yes...)},
		{op: "ciVarsList", words: "ci vars list"},
		{op: "ciVarPut", words: "ci vars set", args: []string{"MODE"}, flags: f("value", "x")},
		{op: "ciVarDelete", words: "ci vars delete", args: []string{"MODE"}, flags: f(yes...)},
		{op: "orgCiSecretsList", words: "ci secrets list", flags: f("org-wide", "true")},
		{op: "orgCiSecretPut", words: "ci secrets set", args: []string{"TOKEN_X"}, flags: f("org-wide", "true"), stdin: "v\n"},
		{op: "orgCiSecretDelete", words: "ci secrets delete", args: []string{"TOKEN_X"}, flags: f("org-wide", "true", "yes", "true")},
		{op: "orgCiVarsList", words: "ci vars list", flags: f("org-wide", "true")},
		{op: "orgCiVarPut", words: "ci vars set", args: []string{"MODE"}, flags: f("org-wide", "true", "value", "x")},
		{op: "orgCiVarDelete", words: "ci vars delete", args: []string{"MODE"}, flags: f("org-wide", "true", "yes", "true")},
		{op: "runnersList", words: "ci runners"},
		{op: "previewsList", words: "previews list"},
		{op: "previewsCreate", words: "previews create", flags: f("pr", "7", "ref", "f", "sha", "abc")},
		{op: "previewsDelete", words: "previews delete", args: []string{"7"}, flags: f(yes...)},
		{op: "membersList", words: "members list"},
		{op: "memberPut", words: "members set", args: []string{"user-1"}, flags: f("role", "deployer")},
		{op: "memberDelete", words: "members remove", args: []string{"user-1"}, flags: f(yes...)},
		{op: "envsList", words: "environments list"},
		{op: "envGet", words: "environments show", args: []string{"production"}},
		{op: "envCreate", words: "environments create", args: []string{"staging"}, flags: f("kind", "staging", "branches", "main,release/**", "reviewers", "role:admin", "approvals", "1", "window", "mon-fri 09:00-18:00 America/Lima")},
		{op: "envPut", words: "environments set", args: []string{"production"}, flags: f("approvals", "2")},
		{op: "envDelete", words: "environments delete", args: []string{"staging"}, flags: f(yes...)},
		{op: "envDeployments", words: "environments history", args: []string{"production"}},
		{op: "promotionCreate", words: "promote", flags: f("to", "production", "sha", "abcdef1", "from", "staging", "comment", "c")},
		{op: "promotionGet", words: "promote show", args: []string{uuidA}},
		{op: "promotionCancel", words: "promote cancel", args: []string{uuidA}, flags: f(yes...)},
		{op: "approvalsPending", words: "approvals list"},
		{op: "promotionApprove", words: "approvals approve", args: []string{"1111"}, flags: f("message", "ok")},
		{op: "promotionReject", words: "approvals reject", args: []string{uuidA}, flags: f("message", "no")},
		{op: "securityOverview", words: "security overview"},
		{op: "alertsList", words: "security alerts list", flags: f("severity", "high", "tool", "semgrep")},
		{op: "alertGet", words: "security alerts show", args: []string{uuidA}},
		{op: "alertDismiss", words: "security alerts dismiss", args: []string{uuidA}, flags: f("reason", "false_positive", "why", "not reachable from outside", "expires", "90d")},
		{op: "alertReopen", words: "security alerts reopen", args: []string{uuidA}},
		{op: "suppressionsList", words: "security suppressions list"},
		{op: "suppressionCreate", words: "security suppressions add", flags: f("rule-id", "r1", "reason", "wont_fix", "why", "accepted by security", "expires", "30d")},
		{op: "suppressionDelete", words: "security suppressions remove", args: []string{uuidA}, flags: f(yes...)},
		{op: "securityPolicyGet", words: "security policy show"},
		{op: "securityPolicyPut", words: "security policy set", flags: f("block-merge-on", "dependency=critical,secret=any", "require-tools", "semgrep,gitleaks")},
		{op: "scanTrigger", words: "security scan", flags: f("tools", "semgrep")},
		{op: "sarifUpload", words: "security sarif upload", args: []string{"@sarif"}, flags: f("sha", "abc", "ref", "main", "tool", "semgrep")},
		{op: "sarifStatus", words: "security sarif status", args: []string{uuidA}},
		{op: "renovateGet", words: "security renovate show"},
		{op: "renovatePut", words: "security renovate enable", flags: f("schedule", "weekly")},
		{op: "renovateRun", words: "security renovate run"},
		{op: "webhooksList", words: "webhooks list"},
		{op: "webhookCreate", words: "webhooks create", flags: f("kind", "slack", "url", "https://hooks.example.com/x", "events", "pr.merged")},
		{op: "webhookUpdate", words: "webhooks update", args: []string{uuidA}, flags: f("active", "false")},
		{op: "webhookDelete", words: "webhooks delete", args: []string{uuidA}, flags: f(yes...)},
		{op: "webhookTest", words: "webhooks test", args: []string{uuidA}},
		{op: "webhookRotate", words: "webhooks rotate-secret", args: []string{uuidA}, flags: f(yes...)},
		{op: "deliveriesList", words: "webhooks deliveries", args: []string{uuidA}},
		{op: "deliveryRedeliver", words: "webhooks redeliver", args: []string{uuidA, uuidB}},
		{op: "orgWebhooksList", words: "webhooks list", flags: f("org-wide", "true")},
		{op: "orgWebhookCreate", words: "webhooks create", flags: f("org-wide", "true", "kind", "email", "email-to", "a@example.com", "events", "deploy.failed")},
		{op: "orgWebhookUpdate", words: "webhooks update", args: []string{uuidA}, flags: f("org-wide", "true", "active", "false")},
		{op: "orgWebhookDelete", words: "webhooks delete", args: []string{uuidA}, flags: f(append([]string{"org-wide", "true"}, yes...)...)},
		{op: "orgWebhookTest", words: "webhooks test", args: []string{uuidA}, flags: f("org-wide", "true")},
		{op: "orgWebhookRotate", words: "webhooks rotate-secret", args: []string{uuidA}, flags: f(append([]string{"org-wide", "true"}, yes...)...)},
		{op: "orgDeliveriesList", words: "webhooks deliveries", args: []string{uuidA}, flags: f("org-wide", "true")},
		{op: "orgDeliveryRedeliver", words: "webhooks redeliver", args: []string{uuidA, uuidB}, flags: f("org-wide", "true")},
		{op: "gitTokensRevokeSession", words: "logout"},
		{op: "notificationsList", words: "notifications list", flags: f("unread", "true")},
		{op: "notificationsMark", words: "notifications read", flags: f("all", "true")},
		{op: "notifSettingsGet", words: "notifications settings show"},
		{op: "notifSettingsPut", words: "notifications settings set", flags: f("digest", "daily")},
		{op: "auditLog", words: "audit", flags: f("since", "7d", "action", "git.pr.merge")},
	}
}

func TestCommandsMatchTheContract(t *testing.T) {
	spec := loadSpec(t)
	srv := &contractServer{t: t, spec: spec, seen: map[string]bool{}, bodies: map[string]map[string]any{},
		knownQuery: map[string][]string{"commitsList": {"page"}}, // existing `git commits --page` (kept for compatibility)
		noIdem:     map[string]bool{"previewsCreate": true},      // existing `previews create` sends no key
	}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	old, oldCfg := apiClient, cfg
	apiClient = client.New(hs.URL, "tok")
	defer func() { apiClient, cfg = old, oldCfg }()
	t.Setenv("CAPSULE_ORG_ID", tOrg)
	t.Setenv("CAPSULE_PROJECT_ID", tProj)
	t.Setenv("CAPSULE_TOKEN", "")
	t.Setenv("CI", "true") // never prompt

	sarif := t.TempDir() + "/r.sarif"
	if err := os.WriteFile(sarif, []byte(`{"version":"2.1.0","runs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range contractCases() {
		c := c
		t.Run(c.op, func(t *testing.T) {
			srv.mu.Lock()
			srv.caseSeen = map[string]bool{}
			srv.mu.Unlock()
			cmd, rest, err := rootCmd.Find(strings.Fields(c.words))
			if err != nil || len(rest) > 0 || cmd == rootCmd {
				t.Fatalf("no command %q", c.words)
			}
			args := append([]string(nil), c.args...)
			for i, a := range args {
				if a == "@sarif" {
					args[i] = sarif
				}
			}
			if c.stdin != "" {
				defer withStdin(t, c.stdin)()
			}
			if c.op == "gitCredentialIssue" {
				cfg = &config.Config{Token: "jwt-session"}
			}
			var runErr error
			out := capture(t, func() { runErr = run(t, cmd, args, c.flags) })
			if runErr != nil {
				t.Fatalf("%s %v: %v\n%s", c.words, args, runErr, out)
			}
			srv.mu.Lock()
			ok := srv.caseSeen[c.op]
			seen := keysOf(srv.caseSeen)
			srv.mu.Unlock()
			if !ok {
				t.Fatalf("%s did not call %s (called: %v)", c.words, c.op, seen)
			}
			if c.op == "gitCredentialIssue" {
				if !strings.HasPrefix(out, "username=ana\npassword=cgt_TESTVALUE\npassword_expiry_utc=") || strings.Count(out, "\n") != 3 {
					t.Fatalf("unexpected helper output: %q", out)
				}
			} else if strings.Contains(out, "cgt_TESTVALUE") {
				t.Fatalf("a token leaked into the output: %s", out)
			}
		})
	}
	for id := range spec.byOp {
		if _, ex := excludedOps[id]; ex {
			continue
		}
		if _, res := reservedOps[id]; res {
			if _, mapped := opCommands[id]; !mapped {
				continue
			}
		}
		if !srv.seen[id] {
			t.Errorf("no contract case reached operation %s", id)
		}
	}
}
