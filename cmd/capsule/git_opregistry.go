package main

// opCommands maps every Git Capsule API operationId (docs/git-capsule/
// API.openapi.yaml) to the capsule command that performs it. The contract test
// (TestEveryOperationHasACommand) walks the YAML and fails when an operationId
// is neither here, nor in excludedOps, nor in reservedOps, and when a command
// listed here does not exist in the command tree.
//
// Packages that add commands later (C3 `recover`) register theirs with
// registerOps from their own init().
var opCommands = map[string]string{
	// platform
	"gitStatus":    "git status",
	"gitReconcile": "git reconcile",
	// project Git link (existing commands)
	"gitLinkGet":    "git show",
	"gitLinkPut":    "git link",
	"gitLinkDelete": "git unlink",
	"gitLinkTest":   "git test",
	// repository, code, refs
	"repoGet":       "git repo view",
	"repoProvision": "git repo create",
	"repoUpdate":    "git repo settings",
	"repoTree":      "git tree",
	"repoBlob":      "git cat",
	"repoCompare":   "git compare",
	"branchesList":  "branch list",
	"branchCreate":  "branch create",
	"branchDelete":  "branch delete",
	"tagsList":      "git tags list",
	"tagCreate":     "git tags create",
	"tagDelete":     "git tags delete",
	"commitsList":   "git commits",
	"commitGet":     "git commits show",
	"commitChecks":  "git commits checks",
	"activityList":  "git activity",
	"insights":      "git insights",
	// protection, CODEOWNERS
	"protectionsList":    "git protect list",
	"protectionCreate":   "git protect set",
	"protectionPut":      "git protect set",
	"protectionDelete":   "git protect remove",
	"protectionEvaluate": "git protect evaluate",
	"codeownersGet":      "git codeowners",
	// mirror, import
	"mirrorGet":    "git mirror show",
	"mirrorPut":    "git mirror set",
	"mirrorDelete": "git mirror remove",
	"mirrorSync":   "git mirror sync",
	"importCreate": "git import github",
	"importGet":    "git import status",
	// access: tokens, SSH keys, service tokens, credential helper
	"gitCredentialIssue": "git credential get",
	"gitTokensList":      "git tokens list",
	"gitTokenCreate":     "git tokens create",
	"gitTokenRevoke":     "git tokens revoke",
	"gitSshKeysList":     "git ssh-keys list",
	"gitSshKeyAdd":       "git ssh-keys add",
	"gitSshKeyDelete":    "git ssh-keys remove",
	"serviceTokensList":  "git service-tokens list",
	"serviceTokenCreate": "git service-tokens create",
	"serviceTokenRevoke": "git service-tokens revoke",
	// pull requests
	"pullsList":        "pr list",
	"pullCreate":       "pr create",
	"pullGet":          "pr view",
	"pullUpdate":       "pr edit",
	"pullReady":        "pr ready",
	"pullFiles":        "pr files",
	"pullCommits":      "pr commits",
	"pullChecks":       "pr checks",
	"pullUpdateBranch": "pr update-branch",
	"pullMerge":        "pr merge",
	"autoMergeEnable":  "pr merge",
	"autoMergeDisable": "pr merge",
	"commentsList":     "pr view",
	"commentCreate":    "pr comment",
	"reviewsList":      "pr view",
	"reviewCreate":     "pr review",
	"reviewDismiss":    "pr dismiss-review",
	"reviewersRequest": "pr request-review",
	// Actions
	"workflowsList":     "workflow list",
	"workflowDispatch":  "workflow run",
	"runsList":          "runs list",
	"runGet":            "runs view",
	"jobLogs":           "runs logs",
	"runRerun":          "runs rerun",
	"runCancel":         "runs cancel",
	"artifactsList":     "runs artifacts",
	"ciSecretsList":     "ci secrets list",
	"ciSecretPut":       "ci secrets set",
	"ciSecretDelete":    "ci secrets delete",
	"ciVarsList":        "ci vars list",
	"ciVarPut":          "ci vars set",
	"ciVarDelete":       "ci vars delete",
	"orgCiSecretsList":  "ci secrets list",
	"orgCiSecretPut":    "ci secrets set",
	"orgCiSecretDelete": "ci secrets delete",
	"orgCiVarsList":     "ci vars list",
	"orgCiVarPut":       "ci vars set",
	"orgCiVarDelete":    "ci vars delete",
	"runnersList":       "ci runners",
	// previews, members (existing commands)
	"previewsList":   "previews list",
	"previewsCreate": "previews create",
	"previewsDelete": "previews delete",
	"membersList":    "members list",
	"memberPut":      "members set",
	"memberDelete":   "members remove",
	// environments, promotions, approvals
	"envsList":         "environments list",
	"envGet":           "environments show",
	"envCreate":        "environments create",
	"envPut":           "environments set",
	"envDelete":        "environments delete",
	"envDeployments":   "environments history",
	"promotionCreate":  "promote",
	"promotionGet":     "promote show",
	"promotionCancel":  "promote cancel",
	"promotionApprove": "approvals approve",
	"promotionReject":  "approvals reject",
	"approvalsPending": "approvals list",
	// security
	"securityOverview":  "security overview",
	"alertsList":        "security alerts list",
	"alertGet":          "security alerts show",
	"alertDismiss":      "security alerts dismiss",
	"alertReopen":       "security alerts reopen",
	"suppressionsList":  "security suppressions list",
	"suppressionCreate": "security suppressions add",
	"suppressionDelete": "security suppressions remove",
	"securityPolicyGet": "security policy show",
	"securityPolicyPut": "security policy set",
	"scanTrigger":       "security scan",
	"sarifUpload":       "security sarif upload",
	"sarifStatus":       "security sarif status",
	"renovateGet":       "security renovate show",
	"renovatePut":       "security renovate enable",
	"renovateRun":       "security renovate run",
	// webhooks, notifications, audit
	"webhooksList":           "webhooks list",
	"webhookCreate":          "webhooks create",
	"webhookUpdate":          "webhooks update",
	"webhookDelete":          "webhooks delete",
	"webhookTest":            "webhooks test",
	"webhookRotate":          "webhooks rotate-secret",
	"deliveriesList":         "webhooks deliveries",
	"deliveryRedeliver":      "webhooks redeliver",
	"orgWebhooksList":        "webhooks list",
	"orgWebhookCreate":       "webhooks create",
	"orgWebhookUpdate":       "webhooks update",
	"orgWebhookDelete":       "webhooks delete",
	"orgWebhookTest":         "webhooks test",
	"orgWebhookRotate":       "webhooks rotate-secret",
	"orgDeliveriesList":      "webhooks deliveries",
	"orgDeliveryRedeliver":   "webhooks redeliver",
	"gitTokensRevokeSession": "logout",
	"notificationsList":      "notifications list",
	"notificationsMark":      "notifications read",
	"notifSettingsGet":       "notifications settings show",
	"notifSettingsPut":       "notifications settings set",
	"auditLog":               "audit",
}

// excludedOps are operations no human or agent calls from the CLI, with the reason.
var excludedOps = map[string]string{
	"gitInboundWebhook": "called by the Git provider (HMAC-signed); `capsule api POST` can replay one for debugging",
}

// reservedOps belong to package C3 (`capsule recover`, recover.go). B2 only
// reserves the registry: C3 calls registerOps from its init() and the contract
// test then verifies the commands exist. Until then the test only logs them.
var reservedOps = map[string]string{
	"recoverProject":     "recover plan|apply|verify --project",
	"gitRecoverPlatform": "recover plan|apply|verify --all",
	"gitRecoverGet":      "recover (progress of a platform run)",
}

// registerOps adds operationId -> command mappings (for packages that add
// command groups after B2).
func registerOps(m map[string]string) {
	for k, v := range m {
		opCommands[k] = v
	}
}
