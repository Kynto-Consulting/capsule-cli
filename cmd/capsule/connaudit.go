package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// connWarning mirrors one element of connection_warnings in the deployment
// (docs/connection-audit.md). It never carries secrets.
type connWarning struct {
	ID             string `json:"id"`
	Severity       string `json:"severity"`
	Kind           string `json:"kind"`
	Source         string `json:"source"`
	Endpoint       string `json:"endpoint"`
	Recommendation string `json:"recommendation"`
	Suppressed     bool   `json:"suppressed"`
}

// Exit code of --strict-connections (same family as policy blocks: 5).
const exitConnections = exitBlocked

// lastConn keeps what the poller saw in the final deployment, for the report.
var lastConn struct {
	warnings []connWarning
	errMsg   string
	seen     bool
}

func connAuditObserve(d deployment) {
	lastConn.warnings, lastConn.errMsg, lastConn.seen = d.ConnectionWarnings, d.ErrorMessage, true
}

func useColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func sevColor(sev string, on bool, s string) string {
	if !on {
		return s
	}
	switch sev {
	case "critical":
		return "\x1b[31;1m" + s + "\x1b[0m"
	case "warning":
		return "\x1b[33m" + s + "\x1b[0m"
	}
	return "\x1b[2m" + s + "\x1b[0m"
}

func connCounts(ws []connWarning) (crit, warn, info, supp int) {
	for _, w := range ws {
		switch {
		case w.Suppressed:
			supp++
		case w.Severity == "critical":
			crit++
		case w.Severity == "warning":
			warn++
		default:
			info++
		}
	}
	return
}

// printConnections writes the "Conexiones internas detectadas" block.
func printConnections(out io.Writer, ws []connWarning, color bool) {
	if len(ws) == 0 {
		return
	}
	crit, warn, info, supp := connCounts(ws)
	fmt.Fprintf(out, "\nConexiones internas detectadas (%d critica(s), %d advertencia(s), %d info, %d suprimida(s))\n", crit, warn, info, supp)
	fmt.Fprintln(out, "Estas conexiones no pasan por el gateway: son fragiles y se saltan la autorizacion y la auditoria de Capsule.")
	for _, w := range ws {
		tag := strings.ToUpper(w.Severity)
		if w.Suppressed {
			tag = "SUPRIMIDA"
		}
		fmt.Fprintf(out, "\n  %s  %s  %s\n", sevColor(w.Severity, color && !w.Suppressed, fmt.Sprintf("%-9s", tag)), w.Kind, w.Endpoint)
		fmt.Fprintf(out, "     origen: %s   id: %s\n", w.Source, w.ID)
		if !w.Suppressed && w.Recommendation != "" {
			fmt.Fprintf(out, "     -> %s\n", w.Recommendation)
		}
	}
	fmt.Fprintln(out, "\n  Para aceptar una: capsule connections --suppress <id> --why \"motivo\" [--expires 90d]")
}

// connAnnotations emits GitHub ::warning/::error lines and the step summary.
func connAnnotations(out io.Writer, ws []connWarning) {
	var md strings.Builder
	md.WriteString("### Conexiones internas detectadas\n\n| Severidad | Tipo | Origen | Endpoint | Recomendacion |\n|---|---|---|---|---|\n")
	n := 0
	for _, w := range ws {
		if w.Suppressed {
			continue
		}
		n++
		lvl := "warning"
		if w.Severity == "critical" {
			lvl = "error"
		}
		fmt.Fprintf(out, "::%s title=Conexion interna sin enrutar (%s)::%s en %s -> %s\n", lvl, w.Kind, ghEscape(w.Endpoint), ghEscape(w.Source), ghEscape(w.Recommendation))
		fmt.Fprintf(&md, "| %s | %s | `%s` | `%s` | %s |\n", w.Severity, w.Kind, mdCell(w.Source), mdCell(w.Endpoint), mdCell(w.Recommendation))
	}
	if n > 0 {
		writeStepSummary(md.String())
	}
}

func ghEscape(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}
func mdCell(s string) string { return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s) }

// connAuditFinish prints the report after the deploy poll and applies
// --strict-connections / --ignore-connections to the exit code.
func connAuditFinish(cmd *cobra.Command, pollErr error) error {
	ignore, _ := cmd.Flags().GetBool("ignore-connections")
	strict, _ := cmd.Flags().GetBool("strict-connections")
	if ignore || !lastConn.seen {
		return pollErr
	}
	ws := lastConn.warnings
	printConnections(os.Stdout, ws, useColor(os.Stdout))
	if inGitHubActions() {
		connAnnotations(os.Stdout, ws)
	}
	if pollErr != nil && strings.Contains(lastConn.errMsg, "CONNECTION_POLICY_BLOCKED") {
		return &exitError{code: exitConnections, msg: "CONNECTION_POLICY_BLOCKED: the project policy (strict) blocked this deployment; fix the critical connections above or suppress them", err: pollErr}
	}
	if pollErr == nil && strict {
		if crit, warn, _, _ := connCounts(ws); crit+warn > 0 {
			return codedErr(exitConnections, "--strict-connections: %d critical and %d warning connection(s) not routed through the gateway", crit, warn)
		}
	}
	return pollErr
}

// parseExpiry accepts 90d, 2w, 12h or any time.ParseDuration value.
func parseExpiry(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty expiry")
	}
	mult := map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour}
	if u := s[len(s)-1:]; mult[u] > 0 {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n <= 0 {
			return time.Time{}, fmt.Errorf("invalid --expires %q (use 90d, 2w, 12h)", s)
		}
		return now.Add(time.Duration(n) * mult[u]), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Time{}, fmt.Errorf("invalid --expires %q (use 90d, 2w, 12h)", s)
	}
	return now.Add(d), nil
}

var connectionsCmd = &cobra.Command{
	Use:   "connections",
	Short: "Show internal connections of a deployment that bypass the gateway, or suppress one",
	Long: `Shows the "Conexiones internas detectadas" report of a deployment (default: the latest).

  capsule connections
  capsule connections --deployment <id>
  capsule connections --suppress <finding-id> --why "sidecar propio" --expires 90d`,
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		base := fmt.Sprintf("/api/v1/orgs/%s/projects/%s", orgID, projectID)
		if id, _ := cmd.Flags().GetString("suppress"); id != "" {
			why, _ := cmd.Flags().GetString("why")
			if strings.TrimSpace(why) == "" {
				return usageErr("--why is required with --suppress")
			}
			body := map[string]any{"finding_id": id, "reason": why}
			if e, _ := cmd.Flags().GetString("expires"); e != "" {
				t, err := parseExpiry(e, time.Now())
				if err != nil {
					return usageErr("%v", err)
				}
				body["expires_at"] = t.UTC().Format(time.RFC3339)
			}
			if err := apiClient.Put(base+"/connection-audit/suppressions", body, nil); err != nil {
				return gitErr(err, "connection audit")
			}
			fmt.Printf("Suppressed %s\n", id)
			return nil
		}
		depID, _ := cmd.Flags().GetString("deployment")
		var d deployment
		if depID == "" {
			var list struct {
				Data []deployment `json:"data"`
			}
			if err := apiClient.Get(base+"/deployments?per_page=1", &list); err != nil {
				return gitErr(err, "deployments")
			}
			if len(list.Data) == 0 {
				fmt.Println("No deployments yet.")
				return nil
			}
			d = list.Data[0]
		} else if err := apiClient.Get(base+"/deployments/"+depID, &d); err != nil {
			return gitErr(err, "deployments")
		}
		if len(d.ConnectionWarnings) == 0 {
			fmt.Printf("Deployment %s: no internal connections outside the gateway detected.\n", d.ID)
			return nil
		}
		fmt.Printf("Deployment %s\n", d.ID)
		printConnections(os.Stdout, d.ConnectionWarnings, useColor(os.Stdout))
		return nil
	},
}

func init() {
	connectionsCmd.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	connectionsCmd.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	connectionsCmd.Flags().String("deployment", "", "Deployment ID (default: the latest)")
	connectionsCmd.Flags().String("suppress", "", "Finding id to suppress")
	connectionsCmd.Flags().String("why", "", "Justification for --suppress (required)")
	connectionsCmd.Flags().String("expires", "", "Suppression lifetime, e.g. 90d, 2w, 12h (default: never)")
	rootCmd.AddCommand(connectionsCmd)
}
