package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ── CI / non-interactive detection ───────────────────────────────────────────

// inGitHubActions reports whether we run inside a GitHub Actions job.
func inGitHubActions() bool { return os.Getenv("GITHUB_ACTIONS") == "true" }

// ciMode: plain output (no animation, no prompts). True on GitHub Actions, on
// generic CI=true runners, or when forced with CAPSULE_CI=1.
func ciMode() bool {
	return inGitHubActions() || os.Getenv("CI") == "true" || os.Getenv("CAPSULE_CI") == "1"
}

// nonInteractive: never prompt. CI mode, or a token supplied through the
// environment (CAPSULE_TOKEN) which means a script is driving the CLI.
func nonInteractive() bool { return ciMode() || os.Getenv("CAPSULE_TOKEN") != "" }

// envOrgProject returns CAPSULE_ORG_ID / CAPSULE_PROJECT_ID.
func envOrgProject() (string, string) {
	return strings.TrimSpace(os.Getenv("CAPSULE_ORG_ID")), strings.TrimSpace(os.Getenv("CAPSULE_PROJECT_ID"))
}

// ── KEY=VAL parsing ──────────────────────────────────────────────────────────

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseKeyVal parses "KEY=VALUE" (value may be empty or contain '=').
func parseKeyVal(s string) (string, string, error) {
	i := strings.Index(s, "=")
	if i < 0 {
		return "", "", fmt.Errorf("%q must be in KEY=VALUE format", s)
	}
	k := strings.TrimSpace(s[:i])
	if !envKeyRe.MatchString(k) {
		return "", "", fmt.Errorf("invalid variable name %q (use letters, digits and _)", k)
	}
	return k, s[i+1:], nil
}

// parseEnvFile parses dotenv content: comments, blank lines, optional
// "export " prefix, single/double quoted values.
func parseEnvFile(content string) (map[string]string, error) {
	out := map[string]string{}
	for n, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, err := parseKeyVal(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n+1, err)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"') {
			v = strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\`, `\`).Replace(v[1 : len(v)-1])
		} else if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out[k] = v
	}
	return out, nil
}

// collectDeployEnv merges --env-file then repeated --env (later wins).
func collectDeployEnv(envFile string, pairs []string) (map[string]string, error) {
	out := map[string]string{}
	if envFile != "" {
		data, err := os.ReadFile(envFile)
		if err != nil {
			return nil, fmt.Errorf("reading --env-file: %w", err)
		}
		m, err := parseEnvFile(string(data))
		if err != nil {
			return nil, fmt.Errorf("--env-file %s: %w", envFile, err)
		}
		for k, v := range m {
			out[k] = v
		}
	}
	for _, p := range pairs {
		// An --env value may carry several lines (the GitHub Action passes a
		// multiline block); each non-empty, non-comment line is one KEY=VAL.
		for _, line := range strings.Split(strings.ReplaceAll(p, "\r\n", "\n"), "\n") {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			k, v, err := parseKeyVal(line)
			if err != nil {
				return nil, fmt.Errorf("--env: %w", err)
			}
			out[k] = v
		}
	}
	return out, nil
}

// ── deployment metadata ──────────────────────────────────────────────────────

type deployMeta struct {
	Ref    string `json:"ref,omitempty"`
	SHA    string `json:"sha,omitempty"`
	Actor  string `json:"actor,omitempty"`
	RunURL string `json:"run_url,omitempty"`
	PR     string `json:"pr,omitempty"`
}

func (m deployMeta) empty() bool { return m == deployMeta{} }

// githubMeta fills metadata from the GitHub Actions environment.
func githubMeta() deployMeta {
	if !inGitHubActions() {
		return deployMeta{}
	}
	m := deployMeta{
		Ref:   os.Getenv("GITHUB_HEAD_REF"),
		SHA:   os.Getenv("GITHUB_SHA"),
		Actor: os.Getenv("GITHUB_ACTOR"),
	}
	ref := os.Getenv("GITHUB_REF")
	if m.Ref == "" {
		m.Ref = os.Getenv("GITHUB_REF_NAME")
	}
	if m.Ref == "" {
		m.Ref = strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
	}
	if strings.HasPrefix(ref, "refs/pull/") {
		parts := strings.Split(ref, "/")
		if len(parts) >= 3 {
			if _, err := strconv.Atoi(parts[2]); err == nil {
				m.PR = parts[2]
			}
		}
	}
	// On pull_request events GITHUB_SHA is the synthetic merge commit; the
	// commit the author pushed is in the event payload.
	if p := os.Getenv("GITHUB_EVENT_PATH"); p != "" && m.PR != "" {
		if data, err := os.ReadFile(p); err == nil {
			var ev struct {
				PullRequest struct {
					Head struct {
						SHA string `json:"sha"`
					} `json:"head"`
				} `json:"pull_request"`
			}
			if json.Unmarshal(data, &ev) == nil && ev.PullRequest.Head.SHA != "" {
				m.SHA = ev.PullRequest.Head.SHA
			}
		}
	}
	if repo, run := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID"); repo != "" && run != "" {
		server := os.Getenv("GITHUB_SERVER_URL")
		if server == "" {
			server = "https://github.com"
		}
		m.RunURL = fmt.Sprintf("%s/%s/actions/runs/%s", server, repo, run)
	}
	return m
}

// ── GitHub Actions outputs ───────────────────────────────────────────────────

func appendToFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}

// setGHOutput writes name=value to $GITHUB_OUTPUT (single-line values only).
func setGHOutput(name, value string) {
	p := os.Getenv("GITHUB_OUTPUT")
	if p == "" || strings.ContainsAny(value, "\r\n") {
		return
	}
	if err := appendToFile(p, name+"="+value+"\n"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write GITHUB_OUTPUT: %v\n", err)
	}
}

func writeStepSummary(md string) {
	p := os.Getenv("GITHUB_STEP_SUMMARY")
	if p == "" {
		return
	}
	if err := appendToFile(p, md); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write GITHUB_STEP_SUMMARY: %v\n", err)
	}
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func ciSuccessSummary(deployID, url string, elapsed interface{}) {
	if url != "" {
		setGHOutput("url", url)
	}
	setGHOutput("deployment-id", deployID)
	var b strings.Builder
	b.WriteString("### Capsule deploy: success\n\n")
	if url != "" {
		fmt.Fprintf(&b, "- URL: %s\n", url)
	}
	fmt.Fprintf(&b, "- Deployment: `%s`\n", deployID)
	fmt.Fprintf(&b, "- Time: %v\n", elapsed)
	if m := githubMeta(); m.SHA != "" {
		fmt.Fprintf(&b, "- Commit: `%s` (%s)\n", shortSHA(m.SHA), m.Ref)
	}
	writeStepSummary(b.String())
}

func ciFailSummary(deployID, reason string) {
	if !inGitHubActions() {
		return
	}
	msg := "Capsule deploy failed: " + reason
	if strings.ContainsAny(msg, "\r\n") {
		msg = strings.NewReplacer("\r", " ", "\n", " ").Replace(msg)
	}
	fmt.Printf("::error title=Capsule deploy::%s\n", msg)
	var b strings.Builder
	b.WriteString("### Capsule deploy: failed\n\n")
	fmt.Fprintf(&b, "- Reason: %s\n", reason)
	if deployID != "" {
		fmt.Fprintf(&b, "- Deployment: `%s`\n", deployID)
	}
	writeStepSummary(b.String())
}
