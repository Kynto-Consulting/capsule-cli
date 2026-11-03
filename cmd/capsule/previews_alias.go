package main

// `capsule git previews list|create|delete` (CLI.md 3.1) mirrors the existing
// top-level `capsule previews`. It lives in a file that sorts after gitcmd.go
// so that init() has already registered the previews subcommands.
func init() {
	g := group("previews", "Per-pull-request preview environments")
	for _, c := range previewsCmd.Commands() {
		g.AddCommand(cloneCmd(c, c.Use))
	}
	gitCmd.AddCommand(g)
}
