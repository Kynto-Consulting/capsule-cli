package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// ── capsule api ──────────────────────────────────────────────────────────────
// Authenticated call to the Capsule API (`gh api` for Capsule). For scripts
// and for routes that have no command yet. The path is always relative to the
// configured API URL: the token is never sent to another host.

func init() {
	c := userLeaf("api METHOD PATH", "Call the Capsule API with your credentials (path relative to /api/v1)", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
		method := strings.ToUpper(args[0])
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return usageErr("METHOD must be GET, POST, PUT, PATCH or DELETE, got %q", args[0])
		}
		path := args[1]
		if strings.Contains(path, "://") {
			return usageErr("PATH must be relative (for example /orgs/ORG/projects): the token is only sent to the configured API URL")
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if !strings.HasPrefix(path, "/api/") {
			path = "/api/v1" + path
		}
		f := cmd.Flags()
		fields := map[string]any{}
		raw, _ := f.GetStringArray("raw-field")
		typed, _ := f.GetStringArray("field")
		for _, kv := range raw {
			k, v, err := parseKeyVal(kv)
			if err != nil {
				return usageErr("-f: %v", err)
			}
			fields[k] = v
		}
		for _, kv := range typed {
			k, v, err := parseKeyVal(kv)
			if err != nil {
				return usageErr("-F: %v", err)
			}
			val, err := typedField(v)
			if err != nil {
				return usageErr("-F %s: %v", k, err)
			}
			fields[k] = val
		}
		var body any
		input, _ := f.GetString("input")
		switch {
		case input != "" && len(fields) > 0:
			return usageErr("--input cannot be combined with -f/-F")
		case input != "":
			var b []byte
			var err error
			if input == "-" {
				var s string
				s, err = readAllStdin()
				b = []byte(s)
			} else {
				b, err = os.ReadFile(input)
			}
			if err != nil {
				return usageErr("reading --input: %v", err)
			}
			if !json.Valid(b) {
				return usageErr("--input is not valid JSON")
			}
			body = json.RawMessage(b)
		case len(fields) > 0 && (method == "GET" || method == "DELETE"):
			keys := make([]string, 0, len(fields))
			for k := range fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			v := url.Values{}
			for _, k := range keys {
				v.Set(k, fmt.Sprint(fields[k]))
			}
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			path += sep + v.Encode()
		case len(fields) > 0:
			body = fields
		}
		hdr := map[string]string{}
		hs, _ := f.GetStringArray("header")
		for _, h := range hs {
			i := strings.Index(h, ":")
			if i <= 0 {
				return usageErr("-H must be 'Name: value', got %q", h)
			}
			name := strings.TrimSpace(h[:i])
			if strings.EqualFold(name, "Authorization") {
				return usageErr("-H cannot set Authorization: the CLI session or CAPSULE_TOKEN is used")
			}
			hdr[name] = strings.TrimSpace(h[i+1:])
		}
		var out any
		status, err := apiDo(method, path, body, &out, hdr)
		if err != nil {
			return err
		}
		if inc, _ := f.GetBool("include"); inc {
			fmt.Fprintf(os.Stderr, "HTTP %d\n", status)
		}
		if out == nil {
			return nil
		}
		if expr, _ := f.GetString("jq"); expr != "" {
			_, err := emit(cmd, out)
			return err
		}
		return printJSON(out)
	})
	c.Example = `  capsule api GET /orgs/$ORG/projects/$PROJ/git/branches
  capsule api POST /orgs/$ORG/projects/$PROJ/git/branches -f name=fix/x -f from=main
  capsule api PUT /orgs/$ORG/projects/$PROJ/git/pulls/12/auto-merge -F delete_branch=true -f style=squash`
	c.Flags().StringArrayP("raw-field", "f", nil, "String field key=value (query for GET/DELETE, JSON body otherwise)")
	c.Flags().StringArrayP("field", "F", nil, "Typed field key=value (true/false/null/numbers; @file reads a file)")
	c.Flags().String("input", "", "Send this JSON file (- for stdin) as the body")
	c.Flags().StringArrayP("header", "H", nil, "Extra header 'Name: value' (not Authorization)")
	c.Flags().Bool("include", false, "Print the HTTP status to stderr")
	rootCmd.AddCommand(c)
}

// typedField converts a -F value: true/false/null, integers, floats, @file, else string.
func typedField(v string) (any, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if strings.HasPrefix(v, "@") {
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return nil, err
		}
		return string(b), nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, nil
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil && strings.ContainsAny(v, ".eE") {
		return n, nil
	}
	return v, nil
}
