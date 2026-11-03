package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// Shared helpers for the platform commands (oauth, members, git, previews,
// ports, backups, platform). The backend contracts are young, so response
// shapes are read leniently (arrays or {"data": [...]}, several field names).

type rec = map[string]any

// platErr turns the backend's status codes into actionable messages. what is
// a short feature name ("project Git", "backups", ...).
func platErr(err error, what string) error {
	if err == nil {
		return nil
	}
	m := err.Error()
	switch {
	case strings.Contains(m, "API error 403"):
		return fmt.Errorf("%w\n  Not allowed: %s needs a full-access session or token with enough role (org/project admin). Tokens with scope \"deploy\" can only deploy; use `capsule login` or a full-access token", err, what)
	case strings.Contains(m, "API error 501"):
		return fmt.Errorf("%w\n  %s is not enabled on this Capsule backend (the server answered 501 NOT IMPLEMENTED); ask the platform administrator", err, what)
	case strings.Contains(m, "API error 404"):
		return fmt.Errorf("%w\n  Not found: either this Capsule server does not support %s yet (older backend) or the org/project/resource does not exist; check ids and `capsule platform`", err, what)
	}
	return err
}

func wantJSON(cmd *cobra.Command) bool {
	o, err := cmd.Flags().GetString("output")
	if err != nil {
		o, _ = rootCmd.PersistentFlags().GetString("output")
	}
	return strings.EqualFold(o, "json")
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// getRaw GETs path into a generic value.
func getRaw(path string) (any, error) {
	var v any
	if err := apiClient.Get(path, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// asList extracts a list of records from an array or {"data"|key: [...]}.
func asList(v any, keys ...string) []rec {
	var arr []any
	switch t := v.(type) {
	case []any:
		arr = t
	case map[string]any:
		for _, k := range append(keys, "data", "items") { // "items" is the B1 envelope
			if a, ok := t[k].([]any); ok {
				arr = a
				break
			}
		}
	}
	out := make([]rec, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// str returns the first non-empty field among keys, formatted for display.
func str(m rec, keys ...string) string {
	for _, k := range keys {
		switch t := m[k].(type) {
		case nil:
		case string:
			if t != "" {
				return t
			}
		case float64:
			if t == float64(int64(t)) {
				return fmt.Sprintf("%d", int64(t))
			}
			return fmt.Sprintf("%g", t)
		case bool:
			return fmt.Sprintf("%t", t)
		default:
			b, _ := json.Marshal(t)
			return string(b)
		}
	}
	return ""
}

func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func newTW() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0) }

// pickByPrefix matches an id (full or prefix) in recs using idKey.
func pickByPrefix(recs []rec, ref, kind string, idKeys ...string) (rec, error) {
	var m []rec
	for _, r := range recs {
		id := str(r, idKeys...)
		if id == ref {
			return r, nil
		}
		if id != "" && strings.HasPrefix(id, ref) {
			m = append(m, r)
		}
	}
	switch len(m) {
	case 0:
		return nil, fmt.Errorf("no %s with id starting %q", kind, ref)
	case 1:
		return m[0], nil
	}
	return nil, fmt.Errorf("%s id prefix %q is ambiguous (%d match); use more characters", kind, ref, len(m))
}

// readStdinSecret reads a secret from stdin (never echoed, never printed).
func readStdinSecret(what string) (string, error) {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading %s from stdin: %w", what, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("no %s received on stdin", what)
	}
	return v, nil
}

func addOrgOnlyFlag(c *cobra.Command) {
	c.Flags().String("org", "", "Org ID (overrides CAPSULE_ORG_ID and .capsule.json)")
}

// projBase returns /api/v1/orgs/{o}/projects/{p}; a short project ID is expanded.
func projBase(cmd *cobra.Command) (string, error) {
	o, p, err := resolveOrgProject(cmd)
	if err != nil {
		return "", err
	}
	if len(p) < 36 {
		id, err := resolveIDPrefix("/api/v1/orgs/"+o+"/projects", "project", p)
		if err != nil {
			return "", err
		}
		p = id
	}
	return fmt.Sprintf("/api/v1/orgs/%s/projects/%s", o, p), nil
}

func abortUnless(cmd *cobra.Command, label string) error {
	if !confirmOrYes(cmd, label) {
		return fmt.Errorf("aborted (pass --yes to confirm non-interactively)")
	}
	return nil
}

// ── capsule platform ─────────────────────────────────────────────────────────

var platformCmd = &cobra.Command{
	Use:   "platform",
	Short: "Show what this Capsule platform supports (backups, git, ports, OIDC)",
	RunE: func(cmd *cobra.Command, args []string) error {
		v, err := getRaw("/api/v1/platform/capabilities")
		if err != nil {
			return platErr(err, "the capabilities endpoint")
		}
		if wantJSON(cmd) {
			return printJSON(v)
		}
		m, _ := v.(map[string]any)
		sub := func(k string) rec { r, _ := m[k].(map[string]any); return r }
		line := func(name string, r rec, detail string) {
			state := "disabled"
			if b, _ := r["enabled"].(bool); b {
				state = "enabled"
			}
			if detail != "" && state == "enabled" {
				state += "  (" + detail + ")"
			}
			fmt.Printf("%-8s %s\n", name, state)
		}
		b, g, p, o := sub("backups"), sub("git"), sub("ports"), sub("oidc")
		line("backups", b, joinKV("default schedule", str(b, "default_schedule")))
		line("git", g, joinKV("url", str(g, "url"), "ssh host", str(g, "ssh_host")))
		line("ports", p, joinKV("range", str(p, "range"), "host", str(p, "host")))
		line("oidc", o, joinKV("issuer", str(o, "issuer")))
		return nil
	},
}

func joinKV(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			parts = append(parts, kv[i]+" "+kv[i+1])
		}
	}
	return strings.Join(parts, ", ")
}

func init() {
	rootCmd.AddCommand(platformCmd)
}
