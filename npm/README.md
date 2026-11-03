# @kyntocg/capsule-cli

Command-line client for **Capsule**, a self-hosted cloud platform on AWS: deploy containers, manage databases,
variables, workers, crons and domains, and run CI/CD from GitHub Actions.

```bash
npm install -g @kyntocg/capsule-cli
capsule login --email you@example.com --password '…'
capsule link          # or create a project: capsule projects create --org <org-id> --name my-app
capsule deploy
```

Highlights: account / project / deploy variables with secrets · scoped deploy tokens · `capsule ci init` for
GitHub Actions and Forgejo · Git linking, members, previews, extra ports, backups · database access control and app IP allowlists from the CLI · short IDs everywhere.
See [Using Capsule from an AI agent](#using-capsule-from-an-ai-agent) at the end to hand this CLI to an agent.

---

## Configuration

The CLI stores its configuration in `~/.capsule/config.yaml`. On first run you will be prompted for your Capsule API URL (default: `http://localhost:8080`).

| Field           | Description                            |
|-----------------|----------------------------------------|
| `api_url`       | Capsule API base URL                   |
| `token`         | JWT access token (written by `login`)  |
| `refresh_token` | Refresh token (written by `login`)     |
| `org_id`        | Default organization ID (optional)     |

You can override the API URL for a single command with the `--api-url` flag:

```bash
capsule --api-url https://api.example.com projects list --org <org-id>
```

---

## Authentication

```bash
capsule login --email you@example.com --password yourpassword
```

The access and refresh tokens are saved to `~/.capsule/config.yaml`. Tokens are refreshed automatically on expiry.

---

## Quick Start

```bash
# 1. Authenticate
capsule login --email you@example.com --password yourpassword

# 2. Create an organization
capsule orgs create --name "Acme Corp" --slug acme

# 3. Create a project
capsule projects create --org <org-id> --name "my-api" --slug my-api --runtime go

# 4. Deploy (from inside your project directory)
cd /path/to/your/project
capsule deploy
```

`capsule deploy` auto-detects your project type (Docker, Node.js, Go, Python, static) and runs an interactive setup on first use, saving the result to `.capsule.json` in your project directory.

---

## Command Reference

### Top-level

| Command | Description |
|---------|-------------|
| `capsule login` | Authenticate with Capsule |
| `capsule deploy` | Deploy the project in the current directory |
| `capsule link` | Link the current directory to an existing Capsule project |

### `orgs` — Organizations

| Command | Description |
|---------|-------------|
| `capsule orgs list` | List your organizations |
| `capsule orgs create` | Create a new organization |

### `projects` — Projects

| Command | Description |
|---------|-------------|
| `capsule projects list` | List projects for an org |
| `capsule projects create` | Create a new project |
| `capsule projects delete [slug]` | Permanently delete a project and all its resources |

### `deployments` — Deployments

| Command | Description |
|---------|-------------|
| `capsule deployments list` | List deployments for a project |
| `capsule deployments get` | Show details of a specific deployment |
| `capsule deployments logs` | Stream build/deploy logs for a deployment |
| `capsule deployments cancel` | Cancel an in-progress deployment |
| `capsule deployments rollback` | Re-deploy the previous successful deployment |

### `logs` — Runtime logs

| Command | Description |
|---------|-------------|
| `capsule logs runtime` | Stream container stdout/stderr (Docker projects) |
| `capsule logs build [deployment-id]` | Show build logs (defaults to latest deployment) |
| `capsule logs lambda` | Show serverless execution logs |
| `capsule logs workers <worker-id>` | Show worker container logs |
| `capsule logs storage` | Show S3/database access logs |
| `capsule logs cron` | Show cron job execution logs |

Add `-f` / `--follow` to any `logs` subcommand to poll continuously.

### `env` — Environment variables

| Command | Description |
|---------|-------------|
| `capsule env list` | List all environment variables |
| `capsule env set KEY=VALUE` | Create or update a variable |
| `capsule env get KEY` | Print the value of a variable |
| `capsule env delete KEY` | Delete a variable |
| `capsule env pull` | Download variables to a `.env` file |
| `capsule env push` | Upload variables from a `.env` file |


### `vars` — Account (organization) variables

Variables shared by every project of an organization. Precedence at deploy time: deploy variables (`capsule deploy --env`) > project variables (`capsule env`) > account variables.

| Command | Description |
|---------|-------------|
| `capsule vars list [--reveal]` | List account variables (secrets masked unless `--reveal`) |
| `capsule vars set KEY=VALUE [--secret] [--scope build\|runtime\|both]` | Create or update variables |
| `capsule vars delete KEY` | Delete a variable |

The org comes from `--org`, `CAPSULE_ORG_ID` or `.capsule.json`.

### `tokens` — API tokens

```bash
# Token that can only deploy one project (for CI)
capsule tokens create --name github-ci --scope deploy --project <project-id>
capsule tokens list
capsule tokens revoke <token-id>
```

### `ci init` — GitHub Actions pipeline

```bash
capsule ci init                                   # detect stack, write .github/workflows/capsule.yml
capsule ci init --working-directory apps/web      # monorepo app
capsule ci init --print                           # preview only
capsule ci init --set-secrets --org <id> --project <id>   # also create the deploy token and set secrets with gh
```

Generates three jobs: `ci` (install, lint, test, build; only steps that exist in the project; Node with npm, pnpm, yarn or bun, Go, Python), `preview` on pull requests and `deploy` on push to the main branch (after `ci`, using `kynto-consulting/capsule/actions/deploy`). Capsule has no per-PR environments, so `preview` deploys to a separate preview project named by the `CAPSULE_PREVIEW_PROJECT_ID` variable and is skipped without it. Flags: `--stack`, `--branch`, `--action-ref`, `--force`, `--repo`.

Secrets used by the workflow: `CAPSULE_TOKEN`, `CAPSULE_ORG_ID`, `CAPSULE_PROJECT_ID`; variable: `CAPSULE_API_URL`. See `actions/deploy/README.md` for full examples.

### Deploy options and CI mode

```bash
capsule deploy --env API_URL=https://api.example.com --env DEBUG=1   # deploy-only variables, repeatable
capsule deploy --env-file .env.production --ref main --sha $(git rev-parse HEAD) --pr 42
```

`--env`/`--env-file` apply to that deployment only and override project and account variables. `--ref`, `--sha` and `--pr` are recorded as deployment metadata; locally, `deploy` warns on stderr when the working tree has uncommitted changes.

Non-interactive authentication (no `~/.capsule/config.yaml`, no `.capsule.json`, no prompts) through environment variables, which take precedence over the config file and flags win over them:

| Variable | Meaning |
|----------|---------|
| `CAPSULE_TOKEN` | `cap_...` API token |
| `CAPSULE_API_URL` | API base URL (required when no config file exists) |
| `CAPSULE_ORG_ID` | Organization ID |
| `CAPSULE_PROJECT_ID` | Project ID |
| `CAPSULE_APPS_DOMAIN` | Apps domain, to build the public URL if the API does not return one |
| `CAPSULE_CI=1` | Force CI mode outside GitHub Actions |

On GitHub Actions (`GITHUB_ACTIONS=true`) `deploy` prints plain progress (no animation), fails with a non-zero exit code on `failed`, `cancelled`, `error` or `timeout`, prints the final URL, writes `url=` and `deployment-id=` to `$GITHUB_OUTPUT` and a summary to `$GITHUB_STEP_SUMMARY`, and fills the metadata (`ref`, `sha`, `actor`, `run_url`, `pr`) from the `GITHUB_*` variables.

### `domains` — Custom domains

| Command | Description |
|---------|-------------|
| `capsule domains list` | List domains attached to a project |
| `capsule domains add <domain>` | Add a custom domain |
| `capsule domains verify <id>` | Verify DNS ownership for a domain |
| `capsule domains remove <id>` | Remove a custom domain |

---

### `db` and `network` — Database access and IP allowlists

Tokens with scope `deploy` cannot use these commands (the API answers 403 and the CLI says so). If the backend has no `APP_SECURITY_GROUP_ID` it answers 501 and network control is unavailable. `--org`, `--project` and `CAPSULE_TOKEN` / `CAPSULE_ORG_ID` / `CAPSULE_PROJECT_ID` work as in the other commands. `<db>` is a database name or the short ID from `capsule db list`.

```bash
capsule db list
capsule db access list mydb                       # rules + connection string (no password)
capsule db access add mydb --cidr 203.0.113.7 --desc "office"   # bare IP becomes /32
capsule db access add mydb --my-ip                # your current public IP as /32
capsule db access remove mydb --cidr 203.0.113.7/32
capsule db access remove mydb 3fa85f64            # short rule ID
capsule db access set mydb --cidr 10.0.0.0/8 --cidr 198.51.100.7   # replaces all rules
capsule db access add mydb --cidr 0.0.0.0/0 --allow-public         # asks to confirm (skip with --yes)

capsule network show
capsule network allow --cidr 203.0.113.0/24
capsule network allow --my-ip
capsule network deny --cidr 203.0.113.0/24
capsule network set --cidr 203.0.113.0/24 --cidr 198.51.100.7
capsule network clear                             # empty allowlist = public; asks to confirm (--yes)
```

`0.0.0.0/0` (and `::/0`) is rejected unless `--allow-public` is given on `db access`; `network` never accepts it (use `network clear`). Database connections require TLS: use `sslmode=require`.

### Platform features: Git, members, previews, ports, backups, OAuth

These commands need a recent Capsule backend. A `404` or `501` answer means the server does not support the feature yet (the CLI says so); `403` means the token or role is not enough (tokens with scope `deploy` can only deploy). `capsule platform` shows what the server has enabled. `--org`, `--project` and `CAPSULE_TOKEN` / `CAPSULE_ORG_ID` / `CAPSULE_PROJECT_ID` work as everywhere else; list commands accept `--output json`.

```bash
capsule platform                                   # backups / git / ports / oidc: enabled or not

# Git
printf %s "$GIT_TOKEN" | capsule git link --provider forgejo --repo-url https://git.example.com/acme/app \
    --branch main --auto-deploy --previews --token-stdin     # token only via stdin, never echoed
capsule git show                                   # link, webhook URL, auth kind (no credentials)
capsule git remote [--ssh]                         # clone URL + `git remote add capsule <url>`
capsule git repo [--import-url <url>]              # create the repo in the platform's own Git
capsule git test | branches | commits [--branch b --page 2] | pulls [--state open|closed|all]
capsule git unlink                                 # asks to confirm (--yes)

# Project members (roles: admin, deployer, viewer)
capsule members list
capsule members set ana@example.com --role deployer         # user ID or email
capsule members remove ana@example.com                      # asks to confirm (--yes)
capsule org access-mode restricted                          # open | restricted; restricted asks to confirm

# Previews per pull request
capsule previews list
capsule previews create --pr 42 --ref feature/x --sha $(git rev-parse HEAD)
capsule previews delete 42

# Extra TCP ports (private by default; changing them restarts the container)
capsule ports list
capsule ports add --container 5432 --name pg --my-ip        # allowlist with your public IP
capsule ports add --container 8080 --cidr 203.0.113.0/24 --cidr 198.51.100.7
capsule ports add --container 9000                          # private: nobody can connect yet
capsule ports access add 5432 --my-ip --desc laptop
capsule ports access remove 5432 3fa85f64                   # short rule ID or CIDR
capsule ports add --container 7000 --cidr 0.0.0.0/0 --allow-public   # asks to confirm (--yes)
capsule ports remove --container 9000

# Backups (schedule: hourly, 6h, 12h, daily, weekly)
capsule backups show
capsule backups set --enabled=true --schedule daily --retention-days 14 --databases --volumes
capsule backups run
capsule backups list
capsule backups restore <snapshot-id> [--target t]         # overwrites data: asks to confirm (--yes)

# OAuth clients (the secret is printed ONCE, on create)
capsule oauth clients create --name "My app" --redirect-uri https://app.example.com/callback [--public] [--first-party]
capsule oauth clients list | update <id|client_id> --name X --redirect-uri https://... | delete <id|client_id>
```

CIDRs are normalized (`1.2.3.4` becomes `1.2.3.4/32`, host bits are cleared) and `0.0.0.0/0` is rejected unless `--allow-public` is passed and confirmed.

### `ci init --provider forgejo` — Forgejo Actions pipeline

```bash
capsule ci init --provider forgejo     # writes .forgejo/workflows/ci.yml and deploy.yml
```

`ci.yml` has the gates: gitleaks (secrets), lint / types / tests of the detected stack, semgrep (SAST), osv-scanner (dependencies) and trivy (repository). Tool versions are pinned in its `env:` block. `deploy.yml` installs `@kyntocg/capsule-cli` from npm (pinned) and runs `capsule deploy` with the secrets `CAPSULE_TOKEN`, `CAPSULE_ORG_ID`, `CAPSULE_PROJECT_ID` and the variable `CAPSULE_API_URL`. CodeQL and Dependabot do not exist on Forgejo: Semgrep and Renovate replace them. `deploy.yml` does not wait for `ci`, so protect the branch. `--set-secrets` is GitHub-only.

### Git Capsule: clone, pull requests, Actions, security, environments

Everything below talks to `API.openapi.yaml` of Git Capsule (`docs/git-capsule/`). Project scope comes from `.capsule.json`, `--org/--project` or `CAPSULE_ORG_ID`/`CAPSULE_PROJECT_ID`. A server that does not offer a function yet answers 404/501 and the CLI says so; it never improvises. Short 8-character ids are accepted everywhere. Add `--yes` to skip confirmations (without it, non-interactive runs exit 2).

**Output:** table by default; `--output json` or `--json` prints the API object; `--jq '.items[].id'` filters it (subset: `.a.b`, `.[0]`, `.[]`, pipes); `--no-header`; list commands take `--limit --cursor --all -q` (ids only). Async operations take `--wait[=10m]`; commands that change state send an automatic `Idempotency-Key` (`--idempotency-key` to fix it).

**Exit codes** (`capsule help exit-codes`): 0 ok, 1 unclassified, 2 usage, 3 not authenticated, 4 no permission / not found, 5 blocked by a rule (PROTECTED_BRANCH, CHECKS_REQUIRED, REVIEWS_REQUIRED, POLICY_BLOCKED), 6 wait timed out or `--exit-status` failure, 7 Git not ready / engine error, 8 pending approval, 9 concurrency conflict.

```bash
# Clone and credentials (token: short-lived cgt_, scoped to one repo, never written to disk or git config)
capsule git clone ORG/REPO [DIR] [--ssh] [--local] [-- git-args]   # configures the helper, then git clone
capsule git login [--status]            # check the session, configure the helper (idempotent)
capsule git credential get|store|erase  # git-credential protocol; Git calls it, you do not
capsule git setup-gcm --client-id ID    # alternative: Git Credential Manager OAuth keys
capsule git tokens list|create|revoke   # personal tokens (create: --name --scope read|write --repo --expires 30d; value shown once)
capsule git ssh-keys list|add|remove    # add --title T --key-file ~/.ssh/id_ed25519.pub
capsule git service-tokens list|create|revoke   # CI tokens, project scoped
capsule logout                          # revokes this session's short-lived Git tokens

# Repository
capsule git status | repo view|create|import URL|settings | tree [PATH] | cat PATH | compare BASE HEAD
capsule git branches list|create|delete | tags list|create|delete | commits [show|checks SHA]
capsule git protect list|set PATTERN|remove|evaluate --branch B   # set: --require-pr --approvals N --checks "ci / test" --codeowners --no-force-push --block-on-security
capsule git codeowners | mirror show|set|remove|sync | import github --repos a/b --token-stdin | activity | insights
capsule branch list|create NAME [--from REF]|delete NAME|protect NAME|unprotect NAME
capsule repo ls|cat|view|create|clone        # gh-style aliases

# Pull requests (alias: pulls)
capsule pr list [--state --author @me --base] | view N [--comments --web] | create --title T [--head --base --draft --reviewer]
capsule pr edit|close|reopen|ready N | diff N [--web] | files N | commits N | checks N [--watch --required --exit-status]
capsule pr review N --approve|--request-changes|--comment [-b TEXT] [--file F:L:TEXT] | request-review N --user U --team T | comment N -b TEXT
capsule pr update-branch N [--wait] | merge N [--squash|--rebase|--merge|--ff] [--delete-branch] [--auto] [--head-sha SHA] | merge N --disable-auto
capsule pr status | co N | dismiss-review N REVIEW_ID --reason TEXT

# Actions
capsule workflow list | run WORKFLOW [--ref R -f k=v --wait] | new --template ci-node|sast|gitleaks|deploy|trivy
capsule runs list [--workflow --branch --status --event] | view ID [--job J] | logs ID [--job J --follow] | rerun ID [--failed] | cancel ID | watch ID [--exit-status] | artifacts ID
capsule ci secrets|vars list|set NAME|delete NAME [--org-wide]   # value from stdin or --from-env, never argv
capsule ci runners

# Security
capsule security overview | alerts list|show ID|dismiss ID --reason R --why TEXT --expires 90d|reopen ID
capsule security suppressions list|add|remove | policy show|set [--block-merge-on dependency=high,code=high,secret=any] | scan [--wait] | renovate show|enable|disable|run
capsule security sarif upload FILE --sha SHA --ref REF [--tool NAME --pr N]   # gzip+base64 automatic; token scope security:write

# Environments and promotions
capsule environments list|show NAME|create NAME --kind production --target PROJECT --reviewers user:ana@x.com,role:admin --approvals 1 --window "mon-fri 09:00-18:00 America/Lima"|set|delete|history NAME   # alias: envs
capsule promote --to production --sha SHA [--from staging --comment C --wait]   # exit 8 while waiting for approval
capsule promote show ID | cancel ID
capsule approvals list | approve ID -m TEXT | reject ID -m TEXT

# Webhooks, notifications, audit, raw API, aliases
capsule webhooks list|create|update|delete|test|rotate-secret|deliveries|redeliver [--org-wide]
capsule notifications list [--unread] | read [ID...|--all] | settings show|set
capsule audit [--action A --actor U --project P --since 7d --csv]
capsule api GET|POST|PUT|PATCH|DELETE /orgs/ORG/projects/P/git/branches [-f k=v] [-F k=true] [--input FILE]
capsule alias set mine 'pr list --author @me' | list | delete NAME
```

Notes: under the npm wrapper, `capsule git clone|login|setup-gcm` and `pr co` run `git` after the CLI exits (the wrapper only runs `git config credential.*`, `clone`, `fetch` and `checkout`). `capsule git login` writes `credential.<git-url>.helper` as `!capsule git credential` (the leading `!` is what makes Git run it as a command) plus `useHttpPath=true`; on Linux and macOS it also puts Git's in-memory `cache --timeout=3000` in front so Git does not request a token per command. `capsule deployments rollback` now picks the last successful deployment before the one running (or the last success when the newest deployment failed).

### Gateway: `connect`, `tunnel` and `ssh` (native binary only)

```
capsule connect capsule://mi-proyecto/db --listen 127.0.0.1:5433     # local tunnel (default 127.0.0.1:0)
capsule connect mi-proyecto/db --exec -- psql -U app midb             # injects PGHOST/PGPORT (and DATABASE_URL if set)
capsule connect --stdio capsule://mi-proyecto/ssh                     # one connection on stdin/stdout (SSH ProxyCommand)
capsule tunnel mi-proyecto/db --listen 127.0.0.1:5433                 # same as connect --listen
capsule ssh mi-proyecto/ssh -l deploy [-- ssh args]                   # ssh through the gateway; --print shows the ~/.ssh/config block
```

Protocol `capsule/1` (TLS 1.3, ALPN `capsule/1`, `docs/gateway/PROTOCOL.md`). The token comes from `CAPSULE_GATEWAY_TOKEN`, `CAPSULE_TOKEN` or the stored login (a `cap_`, `cgt_`, `cgc_` or `cgs_` token; with only a user session the CLI requests a single-use `cgc_`) and is never printed or placed in a URI or command line. `CAPSULE_GATEWAY_ZONE` sets the zone, `CAPSULE_GATEWAY_CA_FILE` pins a private CA. `?transport=udp` URIs forward UDP. Exit codes: 2 usage, 3 no credentials, 4 not authorized or IP denied, 5 quota, 6 timeout, 1 network/service unavailable; with `--exec` and `ssh` the child's exit code is passed through. These commands open sockets and run processes, so they need the native binary: the WebAssembly build shipped by the npm package stops with an explanatory message (exit 1); with the npm wrapper set `CAPSULE_NATIVE_BIN` to the native binary and the wrapper delegates `connect`, `tunnel` and `ssh` to it. The Go library is `github.com/kynto-consulting/capsule/cli/pkg/gatewayclient`.

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--api-url` | (from config) | Capsule API base URL |
| `--output` | `table` | Output format: `table` or `json` |

---

## Common Workflows

### Deploy a project

```bash
cd my-project
capsule deploy
# Follow the interactive prompts on first run.
# Subsequent runs skip setup and deploy immediately.
```

### Tail runtime logs

```bash
capsule logs runtime --org <org-id> --project <project-id> --follow
```

If the current directory is linked (`.capsule.json` present), `--org` and `--project` can be omitted:

```bash
capsule logs runtime -f
```

### Set environment variables

```bash
# Single variable
capsule env set DATABASE_URL=postgres://...

# Mark as secret (value masked in list output)
capsule env set API_KEY=secret-value --secret

# Bulk upload from a .env file
capsule env push --input .env.production --overwrite
```

### Roll back to the previous deployment

```bash
capsule deployments rollback --org <org-id> --project <project-id>
```

### Add a custom domain

```bash
# 1. Register the domain
capsule domains add my-app.example.com

# 2. Add the CNAME record shown in the output to your DNS provider.

# 3. Verify
capsule domains verify <domain-id>
```

---

## Project Linking

Running `capsule deploy` or `capsule link` in a directory creates a `.capsule.json` file that stores the org and project IDs. All subsequent commands in that directory resolve the project automatically, so `--org` and `--project` flags are optional.

To override the linked project for a single command, pass the flags explicitly:

```bash
capsule deployments list --org <other-org-id> --project <other-project-id>
```

---

## Supported Deploy Types

| Type | Description |
|------|-------------|
| `docker` | 24/7 container — always running |
| `lambda` | Serverless — runs on demand (AWS Lambda) |
| `static` | Static files served from CDN (S3) |

Auto-detection priority: `Dockerfile` → `go.mod` → `package.json` → `requirements.txt` → `index.html` → Docker (default).

---

## Using Capsule from an AI agent

Paste this into the agent's system prompt (or `CLAUDE.md` / `AGENTS.md`). Commands are verified against
`capsule-cli` 0.6.x; when unsure, the agent should run `capsule <command> --help` instead of guessing.

```text
You deploy and operate apps on Capsule with the `capsule` CLI. Never edit servers or databases by hand.

MODEL
- Organization -> projects. Each project is served at https://<slug>.apps.tumi-ai.com.
- A folder is linked to a project by .capsule.json (org_id, project_id); `capsule link` creates it.
- Humans: `capsule login`. Machines/CI: env CAPSULE_TOKEN (cap_... token), CAPSULE_API_URL,
  CAPSULE_ORG_ID, CAPSULE_PROJECT_ID (no files written, no prompts).
- `list` prints 8-char short IDs; crons, workers, `db access`, members, port rules and snapshots accept them.
- `capsule platform` shows which platform features the server has (backups, git, ports, oidc). A 404/501 from
  a platform command means "this server does not support it yet": tell the user, do not work around it.

VARIABLES AND SECRETS ("vault")
There is no separate vault product: variables marked --secret play that role, at three levels.
Precedence (top wins): deploy variables > project variables > account variables.
- Account (all projects of the org): capsule vars list [--reveal] | set K=V [--secret] [--scope build|runtime|both] | delete K
- Project: capsule env list|get|set|delete|pull|push
- Deploy (that deployment only): capsule deploy --env K=V (repeatable) --env-file f
Rules: put shared values in the account, project-specific ones in the project, one-off values in deploy.
Mark everything sensitive with --secret; lists mask it, only `vars list --reveal` (org owner/admin) shows it.
Never print or log secret values. Known limit: only runtime/both scopes are injected today; `build` scope is
not applied to the build, so do not rely on it for NEXT_PUBLIC_* variables.

DEPLOY AND CI/CD
- capsule deploy [--env K=V] [--env-file f] [--ref r] [--sha s] [--pr n] [--timeout 20m] [-y]
  States: queued -> building -> deploying -> success|failed. Rollback: capsule deployments rollback.
- GitHub Actions: `capsule ci init` writes .github/workflows/capsule.yml with jobs ci (install/lint/test/build,
  only steps that exist), preview (pull requests) and deploy (push to main, after ci). Flags: --working-directory
  (monorepo), --print, --stack, --branch, --action-ref, --force.
- Forgejo Actions: `capsule ci init --provider forgejo` writes .forgejo/workflows/ci.yml (gates: gitleaks,
  lint/types/tests of the detected stack, semgrep, osv-scanner, trivy; tool versions pinned in its env block) and
  deploy.yml (installs @kyntocg/capsule-cli from npm and runs `capsule deploy`). CodeQL and Dependabot do not
  exist on Forgejo: Semgrep and Renovate replace them. deploy.yml does not wait for ci: protect the branch.
  --set-secrets is github-only; on Forgejo set the secrets by hand.
- Create a token that can ONLY deploy one project: capsule tokens create --name github-ci --scope deploy
  --project <project-id>. It is shown once: store it straight into a secret, never in a file.
- Secrets: CAPSULE_TOKEN, CAPSULE_ORG_ID, CAPSULE_PROJECT_ID; variable CAPSULE_API_URL
  (`capsule ci init --set-secrets --org .. --project ..` does it with gh).
- Action: kynto-consulting/capsule/actions/deploy. Inputs: token, org-id, project-id, env (multiline K=V),
  api-url, timeout, working-directory, version, cli-package. Outputs: url, deployment-id.
  If the CLI is not on npm, use cli-package: ./vendor/capsule-cli.tgz.
- A deploy-scoped token can only: upload source, create/read/cancel deployments, read logs, read its project's
  env, and create/delete previews. Everything else (account variables, other projects, firewall, git, members,
  ports, backups, tokens) answers 403.
- In CI the deploy exits non-zero on failed/cancelled/error/timeout. Debug with
  `capsule logs build` and `capsule logs runtime`.

GIT AND PREVIEWS PER PULL REQUEST
- Link a repo: capsule git link --provider github|forgejo|gitea|generic --repo-url URL [--branch main]
  [--auto-deploy] [--previews --preview-max N --preview-ttl-hours H] [--token-stdin | --ssh-key-file path]
  The access token is read from STDIN (printf %s "$TOKEN" | capsule git link ... --token-stdin): never put it
  on the command line, never echo it. Credentials are write-only: `git show` never returns them.
- capsule git show | test | unlink | remote [--ssh] | repo [--import-url U] | branches | commits [--branch b
  --page n] | pulls [--state open|closed|all]
  `git remote` prints the clone URL and the exact `git remote add capsule <url>` command (the platform's own Git).
- capsule previews list | create --pr N --ref R --sha S | delete N (a deploy token may create/delete previews).
  With auto deploy + previews enabled on the link, pull requests get their own preview without a workflow.

MEMBERS AND PERMISSIONS PER PROJECT
- capsule members list            effective members and where each one's access comes from
- capsule members set <user-id|email> --role admin|deployer|viewer
- capsule members remove <user-id|email>
- capsule org access-mode open|restricted   open: every org member sees every project; restricted: only
  explicitly added members. Restricted hides projects from everyone not listed: confirm first and add yourself.

WORKERS AND CRONS
- capsule workers create|list|get|delete|start|stop ; capsule logs workers <id>
- capsule crons create --name n --schedule "*/5 * * * *" --command '...' | list | get | delete | trigger
  The command runs inside the project's container and can read its variables (e.g. $CRON_SECRET); wrap it in
  single quotes. Creating or deleting workers/crons needs a normal session, not a deploy token.

PORTS AND FIREWALL (per project, no Terraform)
a) Extra TCP ports of the app (besides HTTPS). PRIVATE by default; every change restarts the container:
   capsule ports list                                     # address host:port assigned by the platform
   capsule ports add --container 5432 [--name pg] [--my-ip | --cidr C ...]   # no IPs = private
   capsule ports access add <container-port> --my-ip|--cidr C [--desc d]
   capsule ports access remove <container-port> <rule-id|cidr>
   capsule ports remove --container 5432
   CIDRs are normalized (bare IP -> /32). 0.0.0.0/0 needs --allow-public plus confirmation (--yes): never do it
   unless the user asks explicitly for that port. Prefer --my-ip and re-run it when a VPN/WARP IP rotates.
b) External access to a project's databases (published on ports 40000-40199, always TLS, CLOSED by default):
   capsule db list
   capsule db access list <db>                         # rules + connection string without password
   capsule db access add <db> --my-ip                  # your current public IP as /32
   capsule db access add <db> --cidr 203.0.113.0/24 --desc "office"
   capsule db access remove <db> --cidr 203.0.113.7/32 # or the short rule ID
   capsule db access set <db> --cidr a --cidr b        # replaces everything (idempotent)
   Connect with sslmode=require to db.tumi-ai.com and the port shown by `list` (self-signed certificate:
   encrypts, but not verify-full). IPv4 only, minimum prefix /8, at most 25 rules per database.
   0.0.0.0/0 needs --allow-public, owner/admin, and a confirmation: never do it unless the user asks explicitly.
   Each rule uses one of the AWS security-group rule quota: remove rules you no longer need.
   Only project databases that run as containers; production-tier (RDS) and S3 return UNSUPPORTED.
c) IP allowlist for the application (who can open it in a browser):
   capsule network show | allow --my-ip | allow --cidr X | deny --cidr X | set --cidr a --cidr b | clear
   Empty list = public. A non-empty list blocks everyone else INCLUDING YOU: add your own IP first and verify
   before saving. IPv4 only, max 50 entries, 0.0.0.0/0 is rejected (use `clear`). Changes can take ~15 s.
   Deploy tokens cannot use these commands (403). A 501 NOT_CONFIGURED means the administrator must set
   APP_SECURITY_GROUP_ID on the server: tell the user, do not try to work around it.

BACKUPS
- capsule backups show                       policy, last runs, next run
- capsule backups set --enabled=true|false --schedule hourly|6h|12h|daily|weekly --retention-days N
  --databases --volumes                      only the flags you pass change
- capsule backups run | list | restore <snapshot-id> [--target t]
  Restoring OVERWRITES current data: confirm with the user first (--yes only after they agree), run `backups run`
  before restoring when the current data matters, and verify the app afterwards.

OAUTH CLIENTS (apps that sign in with Capsule / OIDC)
- capsule oauth clients create --name N --redirect-uri https://... [--public] [--first-party] | list |
  update <id|client_id> | delete <id|client_id>
  The client_secret is printed ONCE by `create`: store it immediately in a secret (capsule vars/env --secret),
  never in git, logs or chat. Redirect URIs must be https (http only for localhost).

GOLDEN RULES
1. Ask before anything irreversible or exposing: deleting projects/databases, `network clear`, --allow-public
   on db or ports, restoring a backup, `org access-mode restricted`, `git unlink`, revoking tokens, changing
   account variables (they affect every project).
2. Least privilege: deploy tokens scoped with --project; never a full-access token in CI.
3. Never expose secrets in logs, PRs or workflow YAML (use secrets.*). Never commit .env or .npmrc. Git tokens
   and SSH keys go through stdin/files, never argv; OAuth and webhook secrets are shown once.
4. Verify after acting: after a deploy check the URL (200) and `capsule logs runtime`; after firewall, port or
   member changes re-run `list`/`show`; after a restore check the app.
5. Say what you could not verify and never invent commands: if it is not here, use --help.
```

### Git Capsule for agents (all commands)

Branch on exit codes (2 usage, 3 login, 4 denied or hidden, 5 rule block, 6 timeout or failed run with `--exit-status`, 7 Git not ready, 8 pending approval, 9 conflict) and read JSON with `--json` or `--jq`. Never pass secrets in arguments: use stdin, `--from-env` or `--token-stdin`. The full command list is in "Git Capsule: clone, pull requests, Actions, security, environments" above and in `capsule --help`. Typical loop: `capsule git clone ORG/REPO`, `capsule branch create fix/x`, commit and push with plain `git`, `capsule pr create --title T`, `capsule pr checks N --watch --exit-status`, `capsule pr merge N --squash --auto`, `capsule promote --to production --sha SHA --wait`.

## Internal connections report

`capsule deploy` ends with a "Conexiones internas detectadas" block when the app connects to Capsule internals without the gateway (published host ports, localhost, docker bridge, `capsule-*` container names, private networks, the metadata service). Secrets are never shown.

```
capsule deploy --strict-connections        # exit 5 if there is any critical/warning finding
capsule deploy --ignore-connections        # do not print the report
capsule connections                        # report of the latest deployment
capsule connections --suppress <id> --why "sidecar propio" --expires 90d
```
