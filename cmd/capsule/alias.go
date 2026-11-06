package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

// ── capsule alias ────────────────────────────────────────────────────────────
// CLI.md section 4. Aliases live in ~/.capsule/config.yaml under `aliases:`
// (like `gh alias`). An alias expands to capsule arguments; `$1`..`$9` take
// positional arguments, the rest are appended. Shell aliases (`!cmd`) are not
// supported: the CLI never runs a shell.

var aliasNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func loadAliases() map[string]string {
	_, _ = config.Load()
	return viper.GetStringMapString("aliases")
}

func saveAliases(m map[string]string) error {
	if _, err := config.Load(); err != nil {
		return err
	}
	home, err := config.HomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".capsule")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	viper.Set("aliases", m)
	return viper.WriteConfigAs(filepath.Join(dir, "config.yaml"))
}

// splitArgs splits a command line with single quotes, double quotes and
// backslash escapes.
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && quote != '\'':
			esc = true
			in = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			in = true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

var positionalRe = regexp.MustCompile(`\$([1-9])`)

// expandAliases rewrites args when the first word is a user alias and not a
// built-in command. It never recurses.
func expandAliases(args []string) []string {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return args
	}
	if c, _, err := rootCmd.Find(args[:1]); err == nil && c != rootCmd {
		return args
	}
	value, ok := loadAliases()[strings.ToLower(args[0])]
	if !ok || strings.HasPrefix(value, "!") {
		return args
	}
	rest := args[1:]
	used := 0
	value = positionalRe.ReplaceAllStringFunc(value, func(m string) string {
		n := int(m[1] - '0')
		if n <= len(rest) {
			if n > used {
				used = n
			}
			return rest[n-1]
		}
		return ""
	})
	exp, err := splitArgs(value)
	if err != nil {
		return args
	}
	return append(exp, rest[used:]...)
}

func init() {
	set := userLeaf("set NAME 'COMMAND ...'", "Create or replace an alias", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
		name := strings.ToLower(args[0])
		if !aliasNameRe.MatchString(name) {
			return usageErr("alias names are lowercase letters, digits and dashes, starting with a letter; got %q", args[0])
		}
		if c, _, err := rootCmd.Find([]string{name}); err == nil && c != rootCmd {
			return usageErr("%q is a built-in command and cannot be shadowed", name)
		}
		if strings.HasPrefix(args[1], "!") {
			return usageErr("shell aliases are not supported: the alias must be capsule arguments")
		}
		if _, err := splitArgs(args[1]); err != nil {
			return usageErr("alias command: %v", err)
		}
		m := loadAliases()
		if m == nil {
			m = map[string]string{}
		}
		m[name] = args[1]
		if err := saveAliases(m); err != nil {
			return err
		}
		fmt.Printf("Alias %s -> capsule %s\n", name, args[1])
		return nil
	})
	list := userLeaf("list", "List aliases", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		m := loadAliases()
		if done, err := emit(cmd, m); done || err != nil {
			return err
		}
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		t := newTable(cmd, "ALIAS", "EXPANDS TO")
		for _, k := range names {
			t.row(k, m[k])
		}
		return t.flush()
	})
	del := userLeaf("delete NAME", "Delete an alias", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		name := strings.ToLower(args[0])
		m := loadAliases()
		if _, ok := m[name]; !ok {
			return usageErr("no alias named %q", args[0])
		}
		delete(m, name)
		if err := saveAliases(m); err != nil {
			return err
		}
		fmt.Printf("Alias %s deleted.\n", name)
		return nil
	})
	rootCmd.AddCommand(group("alias", "Shortcuts for capsule commands (stored in ~/.capsule/config.yaml)", set, list, del))
}
