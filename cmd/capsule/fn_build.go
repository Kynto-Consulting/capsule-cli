package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Images built by Capsule from a directory with a Dockerfile (capsule fn deploy --source, capsule fn build). The directory is
// packed like `capsule deploy` packs a project (.gitignore honoured, node_modules and .git skipped), uploaded to the
// presigned URL of POST .../fn/uploads, built by the deploy worker (one build at a time) and pushed to the ECR repository
// Capsule manages for the function (capsule/fn-<12 hex of the function id>).

type fnBuild struct {
	ID         string  `json:"id"`
	Function   string  `json:"function"`
	Runtime    string  `json:"runtime"`
	Status     string  `json:"status"`
	Image      string  `json:"image"`
	Error      string  `json:"error"`
	Log        string  `json:"log"`
	Attempts   int     `json:"attempts"`
	CreatedAt  string  `json:"created_at"`
	FinishedAt *string `json:"finished_at"`
}

// fnBuildPoll is how often a followed build is polled (a variable for tests).
var fnBuildPoll = 2 * time.Second

// uploadFnSource packs dir and uploads it; it returns the source_key to build.
func uploadFnSource(base, dir string, quiet bool) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", fmt.Errorf("--source %s is not a directory", dir)
	}
	if _, err := os.Stat(filepath.Join(abs, "Dockerfile")); err != nil {
		return "", fmt.Errorf("%s has no Dockerfile: for --runtime lambda start it with FROM public.ecr.aws/lambda/nodejs:22 and CMD [\"index.handler\"]", dir)
	}
	var up struct {
		UploadURL string `json:"upload_url"`
		SourceKey string `json:"source_key"`
	}
	if err := apiClient.Post(base+"/uploads", map[string]any{}, &up); err != nil {
		return "", err
	}
	archive, err := createSourceArchive(abs)
	if err != nil {
		return "", fmt.Errorf("packing %s: %w", dir, err)
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "Uploading %s (%s)...\n", dir, formatBytes(len(archive)))
	}
	if err := uploadToS3(up.UploadURL, archive); err != nil {
		return "", err
	}
	return up.SourceKey, nil
}

// followBuild prints the build's log as it grows until the build ends; a failed build is an error (exit 1).
func followBuild(base, id string) (*fnBuild, error) {
	printed := 0
	for {
		var b fnBuild
		if err := apiClient.Get(base+"/builds/"+id, &b); err != nil {
			return nil, err
		}
		if len(b.Log) < printed { // the server keeps only the tail of a long log
			printed = 0
		}
		fmt.Fprint(os.Stderr, b.Log[printed:])
		printed = len(b.Log)
		switch b.Status {
		case "succeeded":
			return &b, nil
		case "failed":
			return &b, fmt.Errorf("build %s failed: %s", b.ID, b.Error)
		}
		time.Sleep(fnBuildPoll)
	}
}

var fnBuildCmd = &cobra.Command{
	Use:   "build <name> --source <dir>",
	Short: "Build a new image of an existing function from a directory with a Dockerfile (runs and history are kept)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		src, _ := cmd.Flags().GetString("source")
		if src == "" {
			return fmt.Errorf("--source <dir> is required")
		}
		key, err := uploadFnSource(base, src, wantJSON(cmd))
		if err != nil {
			return err
		}
		var b fnBuild
		if err := apiClient.Post(base+"/functions/"+args[0]+"/builds", map[string]any{"source_key": key}, &b); err != nil {
			return err
		}
		return finishBuildCmd(cmd, base, &b)
	},
}

func finishBuildCmd(cmd *cobra.Command, base string, b *fnBuild) error {
	if noWait, _ := cmd.Flags().GetBool("no-wait"); noWait {
		if wantJSON(cmd) {
			return printJSON(b)
		}
		fmt.Printf("Build %s queued: follow it with capsule fn build-log %s --follow\n", b.ID, b.ID)
		return nil
	}
	done, err := followBuild(base, b.ID)
	if done != nil && wantJSON(cmd) {
		_ = printJSON(done)
	} else if err == nil {
		fmt.Printf("Image ready: %s\n", done.Image)
	}
	return err
}

var fnBuildsCmd = &cobra.Command{
	Use:   "builds <name>",
	Short: "Image builds of a function (newest first)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		var list []fnBuild
		if err := apiClient.Get(base+"/functions/"+args[0]+"/builds", &list); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "BUILD\tSTATUS\tCREATED\tIMAGE / ERROR")
		for _, b := range list {
			detail := b.Image
			if b.Status == "failed" {
				detail = b.Error
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.ID, b.Status, b.CreatedAt, detail)
		}
		return tw.Flush()
	},
}

var fnBuildLogCmd = &cobra.Command{
	Use:   "build-log <build-id>",
	Short: "Show the log of a build (--follow until it ends)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := fnBase(cmd)
		if err != nil {
			return err
		}
		if follow, _ := cmd.Flags().GetBool("follow"); follow {
			_, err := followBuild(base, args[0])
			return err
		}
		var b fnBuild
		if err := apiClient.Get(base+"/builds/"+args[0], &b); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(b)
		}
		fmt.Printf("Build %s: %s %s\n%s", b.ID, b.Status, strings.TrimSpace(b.Image+" "+b.Error), b.Log)
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{fnBuildCmd, fnBuildsCmd, fnBuildLogCmd} {
		c.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		c.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	}
	fnBuildCmd.Flags().String("source", "", "directory with a Dockerfile to build (packed like capsule deploy)")
	fnBuildCmd.Flags().Bool("no-wait", false, "return once the build is queued instead of following its log")
	fnBuildLogCmd.Flags().Bool("follow", false, "keep printing until the build ends (exit 1 if it fails)")
	fnCmd.AddCommand(fnBuildCmd, fnBuildsCmd, fnBuildLogCmd)
}
