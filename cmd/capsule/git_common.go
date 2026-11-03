package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Shared plumbing for the Git Capsule command groups (pr, branch, workflow,
// runs, security, environments, ...). Everything here follows
// docs/git-capsule/CLI.md section 2.

// pollInterval is how often --wait / watch commands poll. Tests shorten it.
var pollInterval = 3 * time.Second

// ── command constructors ─────────────────────────────────────────────────────

// leaf builds a runnable command that works on a project (adds --org/--project).
func leaf(use, short string, args cobra.PositionalArgs, run func(cmd *cobra.Command, args []string) error) *cobra.Command {
	c := orgLeaf(use, short, args, run)
	c.Flags().String("project", "", "Project ID (overrides CAPSULE_PROJECT_ID and .capsule.json)")
	return c
}

// orgLeaf builds a runnable command scoped to an org (adds --org only).
func orgLeaf(use, short string, args cobra.PositionalArgs, run func(cmd *cobra.Command, args []string) error) *cobra.Command {
	c := userLeaf(use, short, args, run)
	c.Flags().String("org", "", "Org ID (overrides CAPSULE_ORG_ID and .capsule.json)")
	return c
}

// userLeaf builds a runnable command with no scope flags.
func userLeaf(use, short string, args cobra.PositionalArgs, run func(cmd *cobra.Command, args []string) error) *cobra.Command {
	c := &cobra.Command{
		Use:          use,
		Short:        short,
		Args:         args,
		SilenceUsage: true,
	}
	c.RunE = func(cmd *cobra.Command, a []string) error {
		return gitErr(run(cmd, a), featureOf(cmd))
	}
	return c
}

// group builds a non-runnable command that only groups subcommands.
func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	g := &cobra.Command{Use: use, Short: short, SilenceUsage: true}
	g.AddCommand(subs...)
	return g
}

// featureOf names the top-level group of a command for error hints.
func featureOf(cmd *cobra.Command) string {
	c := cmd
	for c.Parent() != nil && c.Parent().Parent() != nil {
		c = c.Parent()
	}
	return "`capsule " + c.Name() + "`"
}

func addYes(c *cobra.Command) { c.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt") }

// confirmOr asks for confirmation unless --yes. Without --yes in a
// non-interactive context it refuses with exit 2 instead of waiting on stdin.
func confirmOr(cmd *cobra.Command, label string) error {
	if y, _ := cmd.Flags().GetBool("yes"); y {
		return nil
	}
	if nonInteractive() || !confirm(label, false) {
		return usageErr("aborted: %s (pass --yes to confirm non-interactively)", label)
	}
	return nil
}

func addList(c *cobra.Command) {
	c.Flags().Int("limit", 0, "Maximum number of items per page (server default when 0)")
	c.Flags().String("cursor", "", "Pagination cursor from a previous page")
	c.Flags().Bool("all", false, "Follow next_cursor until the list is exhausted")
	c.Flags().BoolP("quiet", "q", false, "Print only IDs")
}

func addWait(c *cobra.Command, what string) {
	c.Flags().Duration("wait", 0, "Wait for "+what+" to finish (10m when given without a value)")
	c.Flags().Lookup("wait").NoOptDefVal = "10m"
}

func addIdem(c *cobra.Command) {
	c.Flags().String("idempotency-key", "", "Idempotency-Key to send (default: stable hash of command, arguments and date)")
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

func apiDo(method, path string, body, out any, hdr map[string]string) (int, error) {
	return apiClient.Do(method, path, body, out, hdr)
}

// idemHeaders builds the Idempotency-Key header for commands that registered
// the flag: explicit value, else sha256(command path, args, changed flags,
// UTC date).
func idemHeaders(cmd *cobra.Command, args []string) map[string]string {
	if cmd.Flags().Lookup("idempotency-key") == nil {
		return nil
	}
	if v, _ := cmd.Flags().GetString("idempotency-key"); v != "" {
		return map[string]string{"Idempotency-Key": v}
	}
	var parts []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f.Name != "wait" && f.Name != "idempotency-key" && f.Name != "yes" {
			parts = append(parts, f.Name+"="+f.Value.String())
		}
	})
	sort.Strings(parts)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", cmd.CommandPath(), strings.Join(args, "\x00"), strings.Join(parts, "\x00"), time.Now().UTC().Format("2006-01-02"))
	return map[string]string{"Idempotency-Key": hex.EncodeToString(h.Sum(nil))[:32]}
}

func q(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func seg(s string) string { return url.PathEscape(s) }

// gp returns /api/v1/orgs/{o}/projects/{p}/git + suffix.
func gp(cmd *cobra.Command, suffix string) (string, error) {
	b, err := projBase(cmd)
	if err != nil {
		return "", err
	}
	return b + "/git" + suffix, nil
}

// pp returns /api/v1/orgs/{o}/projects/{p} + suffix.
func pp(cmd *cobra.Command, suffix string) (string, error) {
	b, err := projBase(cmd)
	if err != nil {
		return "", err
	}
	return b + suffix, nil
}

func op(cmd *cobra.Command, suffix string) (string, error) {
	o, err := resolveOrg(cmd)
	if err != nil {
		return "", err
	}
	return "/api/v1/orgs/" + o + suffix, nil
}

// ── output ───────────────────────────────────────────────────────────────────

func init() {
	pf := rootCmd.PersistentFlags()
	pf.Bool("json", false, "Print the API object as JSON (same as --output json)")
	pf.String("jq", "", "Filter JSON output with a jq subset: .a.b  .[0]  .[]  .a[].b  (pipe with |)")
	pf.Bool("no-header", false, "Omit table headers")
}

func jsonMode(cmd *cobra.Command) bool {
	if b, _ := cmd.Flags().GetBool("json"); b {
		return true
	}
	if s, _ := cmd.Flags().GetString("jq"); s != "" {
		return true
	}
	return wantJSON(cmd)
}

// emit prints v as JSON when JSON output was requested and reports whether it did.
func emit(cmd *cobra.Command, v any) (bool, error) {
	if !jsonMode(cmd) {
		return false, nil
	}
	if expr, _ := cmd.Flags().GetString("jq"); expr != "" {
		res, err := jqEval(expr, v)
		if err != nil {
			return true, usageErr("--jq: %v", err)
		}
		for _, r := range res {
			if s, ok := r.(string); ok {
				fmt.Println(s)
				continue
			}
			b, _ := json.Marshal(r)
			fmt.Println(string(b))
		}
		return true, nil
	}
	return true, printJSON(v)
}

// quietIDs prints only the ids when -q is set.
func quietIDs(cmd *cobra.Command, items []rec, idKeys ...string) bool {
	if cmd.Flags().Lookup("quiet") == nil {
		return false
	}
	if b, _ := cmd.Flags().GetBool("quiet"); !b {
		return false
	}
	if len(idKeys) == 0 {
		idKeys = []string{"id"}
	}
	for _, it := range items {
		fmt.Println(sv(it, idKeys...))
	}
	return true
}

type tableOut struct {
	w      *tabwriter.Writer
	header []string
	cmd    *cobra.Command
}

func newTable(cmd *cobra.Command, header ...string) *tableOut {
	return &tableOut{w: tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0), header: header, cmd: cmd}
}

func (t *tableOut) showHeader() {
	if t.header == nil {
		return
	}
	if nh, _ := t.cmd.Flags().GetBool("no-header"); !nh {
		fmt.Fprintln(t.w, strings.Join(t.header, "\t"))
	}
	t.header = nil
}

func (t *tableOut) row(cells ...string) {
	t.showHeader()
	fmt.Fprintln(t.w, strings.Join(cells, "\t"))
}

func (t *tableOut) flush() error {
	t.showHeader() // no rows: still show the header once
	return t.w.Flush()
}

// sv reads a display string from a record. A key may be a dotted path
// ("author.login"). People ({login,name,email}) render as their login, lists
// as comma-joined values, times unchanged.
func sv(m rec, keys ...string) string {
	for _, k := range keys {
		var cur any = m
		for _, p := range strings.Split(k, ".") {
			mm, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = mm[p]
		}
		if s := fmtVal(cur); s != "" {
			return s
		}
	}
	return ""
}

func fmtVal(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case []any:
		var parts []string
		for _, e := range t {
			if s := fmtVal(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		for _, k := range []string{"login", "name", "email"} {
			if s, ok := t[k].(string); ok && s != "" {
				return s
			}
		}
		b, _ := json.Marshal(t)
		return string(b)
	}
	return fmt.Sprint(v)
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func age(ts string) string {
	if ts == "" {
		return ""
	}
	return formatAge(ts)
}

// ── lists ────────────────────────────────────────────────────────────────────

// pageOf extracts the items of a list response ({items:[...]} or a bare array)
// and its next_cursor.
func pageOf(v any) ([]rec, string) {
	items := asList(v, "items")
	next := ""
	if m, ok := v.(map[string]any); ok {
		next, _ = m["next_cursor"].(string)
	}
	return items, next
}

// fetchList GETs path (which may already hold a query string) honoring
// --limit, --cursor and --all. It returns the items and the raw value of the
// last page (or an object {items, next_cursor} when --all merged pages).
func fetchList(cmd *cobra.Command, path string) ([]rec, any, error) {
	limit, _ := cmd.Flags().GetInt("limit")
	cursor, _ := cmd.Flags().GetString("cursor")
	all, _ := cmd.Flags().GetBool("all")
	var items []rec
	for page := 0; page < 100; page++ {
		p := path
		sep := "?"
		if strings.Contains(p, "?") {
			sep = "&"
		}
		if limit > 0 {
			p += sep + "limit=" + strconv.Itoa(limit)
			sep = "&"
		}
		if cursor != "" {
			p += sep + "cursor=" + url.QueryEscape(cursor)
		}
		var raw any
		if _, err := apiDo("GET", p, nil, &raw, nil); err != nil {
			return nil, nil, err
		}
		got, next := pageOf(raw)
		items = append(items, got...)
		if !all {
			return items, raw, nil
		}
		if next == "" {
			break
		}
		cursor = next
	}
	anyItems := make([]any, len(items))
	for i, it := range items {
		anyItems[i] = it
	}
	return items, map[string]any{"items": anyItems, "next_cursor": nil}, nil
}

// resolveShort expands an 8-character id prefix against the items of listPath.
func resolveShort(listPath, ref, kind string, idKeys ...string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", usageErr("%s id is required", kind)
	}
	allDigits := true
	for _, c := range ref {
		if c < '0' || c > '9' {
			allDigits = false
		}
	}
	if len(ref) >= 36 || allDigits {
		return ref, nil
	}
	if len(idKeys) == 0 {
		idKeys = []string{"id"}
	}
	var raw any
	if _, err := apiDo("GET", listPath, nil, &raw, nil); err != nil {
		return "", err
	}
	items, _ := pageOf(raw)
	r, err := pickByPrefix(items, ref, kind, idKeys...)
	if err != nil {
		return "", usageErr("%v", err)
	}
	return sv(r, idKeys...), nil
}

// ── references ───────────────────────────────────────────────────────────────

func prNumber(s string) (string, error) {
	n := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if n == "" {
		return "", usageErr("pull request number required")
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return "", usageErr("pull request must be a number (12 or #12), got %q", s)
		}
	}
	return n, nil
}

// meLogin resolves "@me" with /auth/me.
func meLogin() (string, error) {
	var me rec
	if err := apiClient.Get("/api/v1/auth/me", &me); err != nil {
		return "", err
	}
	if s := sv(me, "login", "username", "name", "email"); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("cannot resolve @me: the server returned no user name")
}

func resolveMe(v string) (string, error) {
	if v == "@me" {
		return meLogin()
	}
	return v, nil
}

// ── wait / poll ──────────────────────────────────────────────────────────────

// waitDuration returns the --wait value (0 = do not wait).
func waitDuration(cmd *cobra.Command) time.Duration {
	if cmd.Flags().Lookup("wait") == nil {
		return 0
	}
	d, _ := cmd.Flags().GetDuration("wait")
	return d
}

// pollUntil calls fn every pollInterval until it reports done or timeout
// passes (exit 6). Progress lines go to stderr.
func pollUntil(timeout time.Duration, what string, fn func() (done bool, status string, err error)) error {
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		done, status, err := fn()
		if err != nil {
			return err
		}
		if status != last {
			stderrf("%s: %s\n", what, status)
			last = status
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return codedErr(exitTimeout, "timed out after %s waiting for %s (last status: %s)", timeout, what, status)
		}
		time.Sleep(pollInterval)
	}
}

// ── jq subset ────────────────────────────────────────────────────────────────

// jqEval implements the part of jq that scripts need most: field access,
// array index, iteration and pipes. Anything else is rejected, not guessed.
func jqEval(expr string, v any) ([]any, error) {
	cur := []any{v}
	for _, stage := range strings.Split(expr, "|") {
		stage = strings.TrimSpace(stage)
		if !strings.HasPrefix(stage, ".") {
			return nil, fmt.Errorf("unsupported expression %q (supported: .a.b  .[0]  .[]  .a[].b  and |)", stage)
		}
		var next []any
		for _, in := range cur {
			out, err := jqStage(stage[1:], in)
			if err != nil {
				return nil, err
			}
			next = append(next, out...)
		}
		cur = next
	}
	return cur, nil
}

func jqStage(path string, in any) ([]any, error) {
	cur := []any{in}
	i := 0
	for i < len(path) {
		var next []any
		switch {
		case path[i] == '.':
			i++
			continue
		case path[i] == '[':
			j := strings.Index(path[i:], "]")
			if j < 0 {
				return nil, fmt.Errorf("unterminated [ in %q", path)
			}
			idx := path[i+1 : i+j]
			i += j + 1
			for _, c := range cur {
				arr, ok := c.([]any)
				if !ok {
					if c == nil {
						continue
					}
					return nil, fmt.Errorf("cannot index %T", c)
				}
				if idx == "" {
					next = append(next, arr...)
					continue
				}
				n, err := strconv.Atoi(idx)
				if err != nil {
					return nil, fmt.Errorf("bad index %q", idx)
				}
				if n < 0 {
					n += len(arr)
				}
				if n >= 0 && n < len(arr) {
					next = append(next, arr[n])
				}
			}
		default:
			j := i
			for j < len(path) && path[j] != '.' && path[j] != '[' {
				j++
			}
			name := path[i:j]
			i = j
			for _, c := range cur {
				m, ok := c.(map[string]any)
				if !ok {
					if c == nil {
						next = append(next, nil)
						continue
					}
					return nil, fmt.Errorf("cannot read .%s of %T", name, c)
				}
				next = append(next, m[name])
			}
		}
		cur = next
	}
	return cur, nil
}

// ── misc ─────────────────────────────────────────────────────────────────────

// readSecretValue reads a CI secret/variable value from --from-env NAME or
// stdin; variables may also use --value (never secrets: argv is visible).
func readSecretValue(cmd *cobra.Command, allowValueFlag bool) (string, error) {
	if allowValueFlag {
		if v, _ := cmd.Flags().GetString("value"); v != "" {
			return v, nil
		}
	}
	if name, _ := cmd.Flags().GetString("from-env"); name != "" {
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			return "", usageErr("environment variable %s is empty or not set", name)
		}
		return v, nil
	}
	v, err := readStdinSecret("value")
	if err != nil {
		return "", usageErr("%v (pipe it on stdin or use --from-env NAME)", err)
	}
	return v, nil
}

// splitCSV splits "a,b,c" ignoring blanks.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func csvFlag(cmd *cobra.Command, name string) []string {
	v, _ := cmd.Flags().GetStringSlice(name)
	var out []string
	for _, s := range v {
		out = append(out, splitCSV(s)...)
	}
	return out
}

// parseDays turns "30d", "12h", "2w" or a Go duration into a duration.
func parseDays(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, usageErr("empty duration")
	}
	unit := s[len(s)-1]
	if unit == 'd' || unit == 'w' {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n < 0 {
			return 0, usageErr("bad duration %q (use 30d, 12h, 2w)", s)
		}
		mult := 24 * time.Hour
		if unit == 'w' {
			mult = 7 * 24 * time.Hour
		}
		return time.Duration(n) * mult, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, usageErr("bad duration %q (use 30d, 12h, 2w)", s)
	}
	return d, nil
}

// webOpen prints the URL for --web. The CLI never spawns a browser itself (it
// runs under WASM): the URL goes to stdout so the caller can open it.
func webOpen(u string) error {
	if u == "" {
		return fmt.Errorf("the server returned no URL for --web")
	}
	fmt.Println(u)
	return nil
}

func nowUTC() time.Time { return time.Now().UTC() }

// cwdForConfig is the directory .capsule.json is looked up from (the Node
// wrapper passes the real one in CAPSULE_CWD).
func cwdForConfig() string {
	if d := os.Getenv("CAPSULE_CWD"); d != "" {
		return d
	}
	d, _ := os.Getwd()
	return d
}
