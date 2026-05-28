package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// ── templates ────────────────────────────────────────────────────────────────

const claudeMDTemplate = `# Capsule Project

This project is deployed via [Capsule](https://github.com/kynto-consulting/capsule).

## Deploy

` + "```" + `bash
capsule deploy           # build and deploy current directory
capsule deployments list # check status
capsule logs runtime     # tail runtime logs
capsule logs build       # tail build logs
` + "```" + `

## Environment variables

` + "```" + `bash
capsule env list                    # list all vars
capsule env set KEY=VALUE           # add or update
capsule env delete KEY              # remove
` + "```" + `

## Other resources

` + "```" + `bash
capsule domains list                # custom domains
capsule workers list                # background workers
capsule crons list                  # scheduled jobs
capsule storage list                # S3 buckets
` + "```" + `
`

const githubWorkflowTemplate = `name: Deploy to Capsule

on:
  push:
    branches: [main]
  workflow_dispatch:

jobs:
  deploy:
    name: Deploy
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.22"

      - name: Install Capsule CLI
        run: go install github.com/kynto-consulting/capsule/cli/cmd/capsule@latest

      - name: Deploy
        env:
          CAPSULE_TOKEN: ${{ secrets.CAPSULE_TOKEN }}
          CAPSULE_API_URL: ${{ secrets.CAPSULE_API_URL }}
        run: capsule deploy --token "$CAPSULE_TOKEN" --api-url "$CAPSULE_API_URL"
`

const cursorRuleTemplate = `---
description: Capsule deployment context for this project
globs:
  - "**/*"
alwaysApply: true
---

This project is deployed via Capsule.

Key CLI commands:
- **Deploy**: ` + "`capsule deploy`" + `
- **Logs**: ` + "`capsule logs runtime`" + `
- **Env vars**: ` + "`capsule env set KEY=VALUE`" + `
- **Status**: ` + "`capsule deployments list`" + `

Project config is stored in ` + "`.capsule.json`" + ` (gitignored — run ` + "`capsule deploy link`" + ` to re-link).
`

// ── gitignore entries added by init ─────────────────────────────────────────

var initGitignoreEntries = []string{
	".capsule.json",
}

// ── command ──────────────────────────────────────────────────────────────────

var initForce bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold Capsule config files into the current project",
	Long: `Creates editor and CI config files so your project is Capsule-aware:

  .claude/CLAUDE.md                    Claude Code context (deploy commands, etc.)
  .github/workflows/capsule-deploy.yml GitHub Actions deploy workflow
  .cursor/rules/capsule.mdc            Cursor AI rules

Also adds .capsule.json to .gitignore so the local project link is not committed.

Use --force to overwrite existing files.`,
	// Skip auth — this command is entirely local.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}

		files := []struct {
			path    string
			content string
		}{
			{".claude/CLAUDE.md", claudeMDTemplate},
			{".github/workflows/capsule-deploy.yml", githubWorkflowTemplate},
			{".cursor/rules/capsule.mdc", cursorRuleTemplate},
		}

		for _, f := range files {
			full := filepath.Join(cwd, filepath.FromSlash(f.path))
			if err := writeFile(full, f.content, initForce); err != nil {
				fmt.Fprintf(os.Stderr, "  skipped  %s (%v)\n", f.path, err)
			} else {
				fmt.Printf("  created  %s\n", f.path)
			}
		}

		if err := ensureGitignore(cwd, initGitignoreEntries); err != nil {
			fmt.Fprintf(os.Stderr, "  warning  could not update .gitignore: %v\n", err)
		} else {
			fmt.Printf("  updated  .gitignore\n")
		}

		fmt.Println()
		fmt.Println("Done. Next steps:")
		fmt.Println("  1. Run: capsule login")
		fmt.Println("  2. Run: capsule deploy")
		fmt.Println("  3. Add CAPSULE_TOKEN and CAPSULE_API_URL to GitHub → Settings → Secrets")
		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&initForce, "force", false, "Overwrite existing files")
	rootCmd.AddCommand(initCmd)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeFile(path, content string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("already exists (use --force to overwrite)")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func ensureGitignore(dir string, entries []string) error {
	path := filepath.Join(dir, ".gitignore")

	existing := map[string]bool{}
	if f, err := os.Open(path); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			existing[strings.TrimSpace(scanner.Text())] = true
		}
		f.Close()
	}

	var toAdd []string
	for _, e := range entries {
		if !existing[e] {
			toAdd = append(toAdd, e)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "\n# Capsule\n%s\n", strings.Join(toAdd, "\n"))
	return err
}
