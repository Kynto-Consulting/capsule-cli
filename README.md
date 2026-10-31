# Capsule CLI

Manage your Capsule cloud infrastructure from the terminal — deploy projects, tail logs, manage environment variables, configure custom domains, and more.

---

## Installation

### From source

Requires Go 1.22+.

```bash
go install github.com/kynto-consulting/capsule/cli/cmd/capsule@latest
```

### Build locally

```bash
cd cli
go build -o ../bin/capsule ./cmd/capsule
# or from the repo root:
make build-cli
```

The compiled binary is written to `bin/capsule`.

### Pre-built releases

Download a binary for your platform from the [Releases](https://github.com/kynto/capsule/releases) page and place it somewhere on your `PATH`.

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
