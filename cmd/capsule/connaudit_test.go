package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

var sampleWarnings = []connWarning{
	{ID: "aaaaaaaaaaaa", Severity: "critical", Kind: "METADATA", Source: "env:X", Endpoint: "http://169.254.169.254:80", Recommendation: "elimina esta conexion"},
	{ID: "bbbbbbbbbbbb", Severity: "warning", Kind: "LOOPBACK", Source: "file:src/db.ts:3", Endpoint: "redis://***@[::1]:6379", Recommendation: "usa el gateway"},
	{ID: "cccccccccccc", Severity: "warning", Kind: "INTERNAL_DIRECT_PORT", Source: "env:DB", Endpoint: "postgres://db.tumi-ai.com:40001", Recommendation: "usa el gateway", Suppressed: true},
}

func TestPrintConnectionsNoColorOutsideTTY(t *testing.T) {
	var b bytes.Buffer
	printConnections(&b, sampleWarnings, false)
	out := b.String()
	if strings.Contains(out, "\x1b[") {
		t.Fatal("no ANSI without a TTY")
	}
	for _, want := range []string{"Conexiones internas detectadas", "CRITICAL", "METADATA", "env:X", "usa el gateway", "SUPRIMIDA", "--suppress"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	b.Reset()
	printConnections(&b, sampleWarnings, true)
	if !strings.Contains(b.String(), "\x1b[") {
		t.Fatal("color requested")
	}
	b.Reset()
	printConnections(&b, nil, false)
	if b.Len() != 0 {
		t.Fatal("empty report prints nothing")
	}
}

func TestConnAnnotationsAndSummary(t *testing.T) {
	sum := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", sum)
	var b bytes.Buffer
	connAnnotations(&b, sampleWarnings)
	out := b.String()
	if !strings.Contains(out, "::error title=") || !strings.Contains(out, "::warning title=") || strings.Count(out, "\n") != 2 {
		t.Fatalf("annotations: %q", out)
	}
	md, _ := os.ReadFile(sum)
	if !strings.Contains(string(md), "Conexiones internas detectadas") || strings.Contains(string(md), "cccccccccccc") {
		t.Fatalf("summary: %s", md)
	}
	if ghEscape("a\nb%") != "a%0Ab%25" {
		t.Fatal("escape")
	}
}

func finishCmd(strict, ignore bool) *cobra.Command {
	c := &cobra.Command{}
	c.Flags().Bool("strict-connections", strict, "")
	c.Flags().Bool("ignore-connections", ignore, "")
	return c
}

func TestConnAuditFinishExitCodes(t *testing.T) {
	old := lastConn
	defer func() { lastConn = old }()
	lastConn.seen, lastConn.warnings, lastConn.errMsg = true, sampleWarnings, ""
	if err := connAuditFinish(finishCmd(false, false), nil); err != nil {
		t.Fatal("warn mode must not fail the deploy")
	}
	if err := connAuditFinish(finishCmd(true, false), nil); exitCodeFor(err) != 5 {
		t.Fatalf("strict: %v", err)
	}
	if err := connAuditFinish(finishCmd(true, true), nil); err != nil {
		t.Fatal("ignore wins")
	}
	lastConn.warnings = []connWarning{sampleWarnings[2]} // only suppressed
	if err := connAuditFinish(finishCmd(true, false), nil); err != nil {
		t.Fatal("suppressed findings do not trip strict")
	}
	lastConn.warnings, lastConn.errMsg = sampleWarnings[:1], "CONNECTION_POLICY_BLOCKED: x"
	if err := connAuditFinish(finishCmd(false, false), io.ErrUnexpectedEOF); exitCodeFor(err) != 5 {
		t.Fatalf("server block must map to exit 5: %v", err)
	}
}

func TestParseExpiry(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	for in, d := range map[string]time.Duration{"90d": 90 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "12h": 12 * time.Hour} {
		got, err := parseExpiry(in, now)
		if err != nil || got.Sub(now) != d {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "d", "-3d", "abc", "0d"} {
		if _, err := parseExpiry(bad, now); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func TestConnectionsCommandAgainstContract(t *testing.T) {
	var gotPut map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/deployments"):
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "d1", "status": "success", "connection_warnings": sampleWarnings}}})
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/connection-audit/suppressions"):
			json.NewDecoder(r.Body).Decode(&gotPut)
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	apiClient = client.New(srv.URL, "t")
	run := func(args ...string) error {
		connectionsCmd.ResetFlags()
		connectionsCmd.Flags().String("org", "", "")
		connectionsCmd.Flags().String("project", "", "")
		connectionsCmd.Flags().String("deployment", "", "")
		connectionsCmd.Flags().String("suppress", "", "")
		connectionsCmd.Flags().String("why", "", "")
		connectionsCmd.Flags().String("expires", "", "")
		connectionsCmd.SetArgs(args)
		return connectionsCmd.Flags().Parse(args)
	}
	if err := run("--org", "o1", "--project", "p1", "--suppress", "aaaaaaaaaaaa", "--why", "sidecar propio", "--expires", "90d"); err != nil {
		t.Fatal(err)
	}
	if err := connectionsCmd.RunE(connectionsCmd, nil); err != nil {
		t.Fatal(err)
	}
	if gotPut["finding_id"] != "aaaaaaaaaaaa" || gotPut["reason"] != "sidecar propio" || gotPut["expires_at"] == nil {
		t.Fatalf("PUT body %v", gotPut)
	}
	if err := run("--org", "o1", "--project", "p1", "--suppress", "aaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if err := connectionsCmd.RunE(connectionsCmd, nil); exitCodeFor(err) != exitUsage {
		t.Fatalf("--why is required: %v", err)
	}
	if err := run("--org", "o1", "--project", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := connectionsCmd.RunE(connectionsCmd, nil); err != nil {
		t.Fatal(err)
	}
}
