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
GitHub Actions · database access control and app IP allowlists from the CLI · short IDs everywhere.
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
`capsule-cli` 0.5.x; when unsure, the agent should run `capsule <command> --help` instead of guessing.

```text
You deploy and operate apps on Capsule with the `capsule` CLI. Never edit servers or databases by hand.

MODEL
- Organization -> projects. Each project is served at https://<slug>.apps.tumi-ai.com.
- A folder is linked to a project by .capsule.json (org_id, project_id); `capsule link` creates it.
- Humans: `capsule login`. Machines/CI: env CAPSULE_TOKEN (cap_... token), CAPSULE_API_URL,
  CAPSULE_ORG_ID, CAPSULE_PROJECT_ID (no files written, no prompts).
- `list` prints 8-char short IDs; crons, workers and `db access` accept them.

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
- Create a token that can ONLY deploy one project: capsule tokens create --name github-ci --scope deploy
  --project <project-id>. It is shown once: store it straight into a secret, never in a file.
- GitHub secrets: CAPSULE_TOKEN, CAPSULE_ORG_ID, CAPSULE_PROJECT_ID; variable CAPSULE_API_URL
  (`capsule ci init --set-secrets --org .. --project ..` does it with gh).
- Action: kynto-consulting/capsule/actions/deploy. Inputs: token, org-id, project-id, env (multiline K=V),
  api-url, timeout, working-directory, version, cli-package. Outputs: url, deployment-id.
  If the CLI is not on npm, use cli-package: ./vendor/capsule-cli.tgz.
- A deploy-scoped token can only: upload source, create/read/cancel deployments, read logs, read its project's
  env. Everything else (account variables, other projects, firewall, tokens) answers 403.
- There are NO per-PR environments. `preview` deploys to a separate project set in the
  CAPSULE_PREVIEW_PROJECT_ID variable and is skipped without it. Do not promise preview-per-PR.
- In CI the deploy exits non-zero on failed/cancelled/error/timeout. Debug with
  `capsule logs build` and `capsule logs runtime`.

WORKERS AND CRONS
- capsule workers create|list|get|delete|start|stop ; capsule logs workers <id>
- capsule crons create --name n --schedule "*/5 * * * *" --command '...' | list | get | delete | trigger
  The command runs inside the project's container and can read its variables (e.g. $CRON_SECRET); wrap it in
  single quotes. Creating or deleting workers/crons needs a normal session, not a deploy token.

PORTS AND FIREWALL (per project, no Terraform)
There is no way to open an arbitrary TCP port of an app: apps are served only over HTTPS behind the load balancer.
a) External access to a project's databases (published on ports 40000-40199, always TLS, CLOSED by default):
   capsule db list
   capsule db access list <db>                         # rules + connection string without password
   capsule db access add <db> --my-ip                  # your current public IP as /32
   capsule db access add <db> --cidr 203.0.113.0/24 --desc "office"
   capsule db access remove <db> --cidr 203.0.113.7/32 # or the short rule ID
   capsule db access set <db> --cidr a --cidr b        # replaces everything (idempotent)
   Connect with sslmode=require to db.tumi-ai.com and the port shown by `list` (self-signed certificate:
   encrypts, but not verify-full). IPv4 only, minimum prefix /8, at most 25 rules per database.
   0.0.0.0/0 needs --allow-public, owner/admin, and a confirmation: never do it unless the user asks explicitly.
   Many VPN/WARP egress IPs rotate: if a connection stops working, run --my-ip again instead of widening ranges.
   Each rule uses one of the AWS security-group rule quota: remove rules you no longer need.
   Only project databases that run as containers; production-tier (RDS) and S3 return UNSUPPORTED.
b) IP allowlist for the application (who can open it in a browser):
   capsule network show | allow --my-ip | allow --cidr X | deny --cidr X | set --cidr a --cidr b | clear
   Empty list = public. A non-empty list blocks everyone else INCLUDING YOU: add your own IP first and verify
   before saving. IPv4 only, max 50 entries, 0.0.0.0/0 is rejected (use `clear`). Changes can take ~15 s.
   Deploy tokens cannot use these commands (403). A 501 NOT_CONFIGURED means the administrator must set
   APP_SECURITY_GROUP_ID on the server: tell the user, do not try to work around it.

GOLDEN RULES
1. Ask before anything irreversible or exposing: deleting projects/databases, `network clear`, --allow-public,
   revoking tokens, changing account variables (they affect every project).
2. Least privilege: deploy tokens scoped with --project; never a full-access token in CI.
3. Never expose secrets in logs, PRs or workflow YAML (use secrets.*). Never commit .env or .npmrc.
4. Verify after acting: after a deploy check the URL (200) and `capsule logs runtime`; after firewall
   changes re-run `list`/`show`.
5. Say what you could not verify and never invent commands: if it is not here, use --help.
```
