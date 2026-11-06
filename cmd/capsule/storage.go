package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var storageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Object storage (S3-compatible) of a project",
	Long: `Capsule's own object storage: S3-compatible buckets that live on the Capsule server.

  capsule storage create --name files [--public] [--max-size 10GB]
  capsule storage put ./photo.jpg files/img/photo.jpg
  capsule storage ls files/img/
  capsule storage get files/img/photo.jpg ./copy.jpg
  capsule storage presign files/img/photo.jpg --expires 1h
  capsule storage presign files/up.bin --method put

Each bucket has its own access key and secret (capsule storage show --reveal, capsule storage rotate) and
the connection variables are written to the project's environment (S3_*, STORAGE_<NAME>_*).`,
}

// storageItem is one bucket as returned by the API (own-store buckets carry the extra fields).
type storageItem struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Engine           string  `json:"engine"`
	DBName           string  `json:"db_name"`
	Status           string  `json:"status"`
	Host             string  `json:"host"`
	Bucket           string  `json:"bucket"`
	Public           bool    `json:"public"`
	Endpoint         string  `json:"endpoint"`
	InternalEndpoint string  `json:"internal_endpoint"`
	Region           string  `json:"region"`
	AccessKey        string  `json:"access_key"`
	SecretKey        string  `json:"secret_key"`
	UsedBytes        int64   `json:"used_bytes"`
	Objects          int     `json:"objects"`
	MaxBytes         *int64  `json:"max_bytes"`
	ProjectUsedBytes int64   `json:"project_used_bytes"`
	ProjectMaxBytes  int64   `json:"project_max_bytes"`
	OrgUsedBytes     int64   `json:"org_used_bytes"`
	OrgMaxBytes      int64   `json:"org_max_bytes"`
	GatewayURL       string  `json:"gateway_url"`
	RotatedAt        *string `json:"rotated_at"`
}

func (s *storageItem) own() bool { return s.Engine == "objstore" }

func storageBase(orgID, projectID string) string {
	return fmt.Sprintf("/api/v1/orgs/%s/projects/%s/storage", orgID, projectID)
}

// pickStorage matches a bucket by name, full bucket name or ID (prefix).
func pickStorage(items []storageItem, ref string) (*storageItem, error) {
	for i := range items {
		if items[i].Name == ref || items[i].DBName == ref || items[i].Bucket == ref || items[i].ID == ref {
			return &items[i], nil
		}
	}
	var m []*storageItem
	for i := range items {
		if len(ref) >= 4 && strings.HasPrefix(items[i].ID, ref) {
			m = append(m, &items[i])
		}
	}
	switch len(m) {
	case 0:
		return nil, fmt.Errorf("no storage bucket %q in this project (see: capsule storage list)", ref)
	case 1:
		return m[0], nil
	}
	return nil, fmt.Errorf("bucket id prefix %q is ambiguous (%d match)", ref, len(m))
}

func resolveStorage(orgID, projectID, ref string) (*storageItem, error) {
	var resp struct {
		Data []storageItem `json:"data"`
	}
	if err := apiClient.Get(storageBase(orgID, projectID), &resp); err != nil {
		return nil, err
	}
	return pickStorage(resp.Data, ref)
}

// splitObjectRef parses "bucket/key", "s3://bucket/key" or "capsule://project/bucket/key" (project part dropped).
func splitObjectRef(ref string) (bucket, key string) {
	ref = strings.TrimPrefix(ref, "s3://")
	if strings.HasPrefix(ref, "capsule://") {
		parts := strings.SplitN(strings.TrimPrefix(ref, "capsule://"), "/", 3)
		if len(parts) == 3 {
			return parts[1], parts[2]
		}
		if len(parts) == 2 {
			return parts[1], ""
		}
	}
	b, k, _ := strings.Cut(ref, "/")
	return b, k
}

// ownBucketFor resolves the bucket of an object reference and fails for legacy AWS buckets.
func ownBucketFor(cmd *cobra.Command, ref string) (orgID, projectID string, b *storageItem, key string, err error) {
	if orgID, projectID, err = resolveOrgProject(cmd); err != nil {
		return
	}
	name, key := splitObjectRef(ref)
	if name == "" {
		err = fmt.Errorf("expected <bucket>/<key>, got %q", ref)
		return
	}
	if b, err = resolveStorage(orgID, projectID, name); err != nil {
		return
	}
	if !b.own() {
		err = fmt.Errorf("%q is an AWS S3 bucket created before Capsule storage: only presign (s3://bucket/key) and delete work on it", name)
	}
	return
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// parseSize understands 1024, 500MB, 10GB, 1.5GiB, 2T ("0" and "unlimited" mean no limit).
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	if s == "unlimited" || s == "none" {
		return 0, nil
	}
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10}, {"tb", 1 << 40}, {"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10},
		{"t", 1 << 40}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10}, {"b", 1}} {
		if strings.HasSuffix(s, u.suf) {
			mult, s = u.m, strings.TrimSpace(strings.TrimSuffix(s, u.suf))
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q (examples: 500MB, 10GB, 1.5GiB)", s)
	}
	return int64(f * float64(mult)), nil
}

func limitStr(n int64) string {
	if n <= 0 {
		return "unlimited"
	}
	return humanBytes(n)
}

func printBucket(b *storageItem, reveal bool) {
	fmt.Printf("Bucket     %s  (%s)\n", b.Name, b.Bucket)
	vis := "private"
	if b.Public {
		vis = "public read"
	}
	fmt.Printf("Visibility %s\n", vis)
	fmt.Printf("Usage      %s in %d objects", humanBytes(b.UsedBytes), b.Objects)
	if b.MaxBytes != nil {
		fmt.Printf(" (bucket cap %s)", humanBytes(*b.MaxBytes))
	}
	fmt.Println()
	fmt.Printf("Project    %s of %s\n", humanBytes(b.ProjectUsedBytes), limitStr(b.ProjectMaxBytes))
	fmt.Printf("Org        %s of %s\n", humanBytes(b.OrgUsedBytes), limitStr(b.OrgMaxBytes))
	fmt.Printf("Endpoint   %s   (public, path-style)\n", b.Endpoint)
	fmt.Printf("Internal   %s   (apps on the same server)\n", b.InternalEndpoint)
	if b.GatewayURL != "" {
		fmt.Printf("Gateway    %s\n", b.GatewayURL)
	}
	fmt.Printf("Region     %s\n", b.Region)
	fmt.Printf("Access key %s\n", b.AccessKey)
	switch {
	case b.SecretKey != "":
		fmt.Printf("Secret key %s\n", b.SecretKey)
	case reveal:
		fmt.Println("Secret key (your role cannot reveal secrets)")
	default:
		fmt.Println("Secret key ********  (capsule storage show --reveal)")
	}
}

var storageCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a bucket in Capsule's object storage",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		public, _ := cmd.Flags().GetBool("public")
		body := map[string]any{"name": name, "public": public}
		if s, _ := cmd.Flags().GetString("max-size"); s != "" {
			n, err := parseSize(s)
			if err != nil {
				return err
			}
			if n > 0 {
				body["max_bytes"] = n
			}
		}
		var b storageItem
		if err := apiClient.Post(storageBase(orgID, projectID), body, &b); err != nil {
			return err
		}
		fmt.Printf("Created bucket %s (%s)\n\n", b.Name, b.Bucket)
		if !b.own() { // platform without its own store: legacy AWS bucket, provisioned in the background
			fmt.Printf("Status: %s\n", b.Status)
			return nil
		}
		printBucket(&b, true)
		fmt.Println("\nThe project environment now has S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, S3_REGION and")
		fmt.Printf("STORAGE_%s_*: they reach the next deployment. The secret is shown again with: capsule storage show %s --reveal\n",
			strings.ToUpper(strings.ReplaceAll(b.Name, "-", "_")), b.Name)
		return nil
	},
}

var storageListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"buckets"},
	Short:   "List the buckets of a project",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		var resp struct {
			Data []storageItem `json:"data"`
		}
		if err := apiClient.Get(storageBase(orgID, projectID), &resp); err != nil {
			return err
		}
		if out, _ := cmd.Flags().GetString("output"); out == "json" {
			return json.NewEncoder(os.Stdout).Encode(resp.Data)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tBUCKET\tKIND\tVISIBILITY\tUSED\tOBJECTS\tSTATUS")
		for _, b := range resp.Data {
			kind, vis, used, objs := "aws-s3", "-", "-", "-"
			bucket := b.DBName
			if b.own() {
				kind, bucket, used, objs = "capsule", b.Bucket, humanBytes(b.UsedBytes), strconv.Itoa(b.Objects)
				vis = "private"
				if b.Public {
					vis = "public"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", id8(b.ID), b.Name, bucket, kind, vis, used, objs, b.Status)
		}
		return w.Flush()
	},
}

var storageShowCmd = &cobra.Command{
	Use:   "show <bucket>",
	Short: "Show a bucket: endpoints, usage, quotas and credentials",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		item, err := resolveStorage(orgID, projectID, args[0])
		if err != nil {
			return err
		}
		var b storageItem
		if err := apiClient.Get(storageBase(orgID, projectID)+"/"+item.ID, &b); err != nil {
			return err
		}
		reveal, _ := cmd.Flags().GetBool("reveal")
		if !reveal {
			b.SecretKey = ""
		}
		if out, _ := cmd.Flags().GetString("output"); out == "json" {
			return json.NewEncoder(os.Stdout).Encode(b)
		}
		if !b.own() {
			fmt.Printf("Bucket %s (AWS S3, created before Capsule storage) status %s\n", b.DBName, b.Status)
			return nil
		}
		printBucket(&b, reveal)
		return nil
	},
}

var storageRotateCmd = &cobra.Command{
	Use:   "rotate <bucket>",
	Short: "Issue a new access key and secret (the old pair stops working at once)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, _, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !confirm("Rotate the credentials of "+b.Name+"? Apps using the old pair fail until redeployed with the new one.", false) {
			return nil
		}
		var nb storageItem
		if err := apiClient.Post(storageBase(orgID, projectID)+"/"+b.ID+"/rotate", nil, &nb); err != nil {
			return err
		}
		fmt.Println("Credentials rotated; the project environment was updated (redeploy to apply).")
		printBucket(&nb, true)
		return nil
	},
}

var storageUpdateCmd = &cobra.Command{
	Use:   "update <bucket>",
	Short: "Change visibility or the per-bucket size cap",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, _, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		body := map[string]any{}
		if cmd.Flags().Changed("public") {
			v, _ := cmd.Flags().GetBool("public")
			body["public"] = v
		}
		if cmd.Flags().Changed("max-size") {
			s, _ := cmd.Flags().GetString("max-size")
			n, err := parseSize(s)
			if err != nil {
				return err
			}
			body["max_bytes"] = n // 0 clears the cap
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to change: use --public=true|false and/or --max-size")
		}
		var nb storageItem
		if err := apiClient.Patch(storageBase(orgID, projectID)+"/"+b.ID, body, &nb); err != nil {
			return err
		}
		printBucket(&nb, false)
		return nil
	},
}

var storageDeleteCmd = &cobra.Command{
	Use:   "delete <bucket>",
	Short: "Delete a bucket and every object in it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		b, err := resolveStorage(orgID, projectID, args[0])
		if err != nil {
			return err
		}
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !confirm("Delete bucket "+b.Name+" and ALL its objects? This cannot be undone.", false) {
			return nil
		}
		if err := apiClient.Delete(storageBase(orgID, projectID) + "/" + b.ID); err != nil {
			return err
		}
		fmt.Println("Deleted", b.Name)
		return nil
	},
}

type quotaResp struct {
	Scope     string `json:"scope"`
	MaxBytes  int64  `json:"max_bytes"`
	Explicit  bool   `json:"explicit"`
	UsedBytes int64  `json:"used_bytes"`
}

var storageQuotaCmd = &cobra.Command{
	Use:   "quota",
	Short: "Show or set storage quotas (the project and organization caps apply to all their buckets)",
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		set := func(flag, path string) error {
			s, _ := cmd.Flags().GetString(flag)
			n, err := parseSize(s)
			if err != nil {
				return err
			}
			var q quotaResp
			if err := apiClient.Put(path, map[string]any{"max_bytes": n}, &q); err != nil {
				return err
			}
			fmt.Printf("%s quota: %s\n", q.Scope, limitStr(q.MaxBytes))
			return nil
		}
		if cmd.Flags().Changed("project-max") {
			if err := set("project-max", storageBase(orgID, projectID)+"/quota"); err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("org-max") {
			if err := set("org-max", fmt.Sprintf("/api/v1/orgs/%s/storage/quota", orgID)); err != nil {
				return err
			}
		}
		var p, o quotaResp
		if err := apiClient.Get(storageBase(orgID, projectID)+"/quota", &p); err != nil {
			return err
		}
		if err := apiClient.Get(fmt.Sprintf("/api/v1/orgs/%s/storage/quota", orgID), &o); err != nil {
			return err
		}
		if out, _ := cmd.Flags().GetString("output"); out == "json" {
			return json.NewEncoder(os.Stdout).Encode(map[string]quotaResp{"project": p, "org": o})
		}
		for _, q := range []quotaResp{p, o} {
			src := "platform default"
			if q.Explicit {
				src = "set explicitly"
			}
			fmt.Printf("%-8s %s of %s  (%s)\n", q.Scope, humanBytes(q.UsedBytes), limitStr(q.MaxBytes), src)
		}
		return nil
	},
}

var storagePresignCmd = &cobra.Command{
	Use:   "presign <bucket>/<key>",
	Short: "Generate a presigned GET or PUT URL for an object (s3://bucket/key also accepted)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, err := resolveOrgProject(cmd)
		if err != nil {
			return err
		}
		name, key := splitObjectRef(args[0])
		if name == "" || key == "" {
			return fmt.Errorf("expected <bucket>/<object-key>, got %q", args[0])
		}
		b, err := resolveStorage(orgID, projectID, name)
		if err != nil {
			return err
		}
		secs, err := expiresSeconds(cmd)
		if err != nil {
			return err
		}
		method, _ := cmd.Flags().GetString("method")
		url, err := presignURL(orgID, projectID, b.ID, key, method, secs)
		if err != nil {
			return err
		}
		fmt.Println(url)
		return nil
	},
}

// expiresSeconds reads --expires as seconds or a duration (90, 15m, 2h, 7d).
func expiresSeconds(cmd *cobra.Command) (int, error) {
	s, _ := cmd.Flags().GetString("expires")
	if s == "" {
		return 3600, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return n * 86400, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --expires %q (examples: 3600, 15m, 2h, 7d)", s)
	}
	return int(d / time.Second), nil
}

func presignURL(orgID, projectID, bucketID, key, method string, secs int) (string, error) {
	var resp struct {
		URL string `json:"url"`
	}
	body := map[string]any{"key": key, "expires": secs, "method": strings.ToUpper(method)}
	if err := apiClient.Post(storageBase(orgID, projectID)+"/"+bucketID+"/presign", body, &resp); err != nil {
		return "", err
	}
	return resp.URL, nil
}

func init() {
	storageCmd.PersistentFlags().String("org", "", "Org ID")
	storageCmd.PersistentFlags().String("project", "", "Project ID")
	storageCmd.PersistentFlags().StringP("output", "o", "", "Output format: json")

	storageCreateCmd.Flags().String("name", "", "Bucket name (3-31 chars: lowercase letters, digits, '-')")
	storageCreateCmd.Flags().Bool("public", false, "Allow anonymous read (GET) of objects, e.g. for public assets")
	storageCreateCmd.Flags().String("max-size", "", "Cap for this bucket (e.g. 10GB)")
	storageCreateCmd.Flags().BoolP("yes", "y", false, "Accepted for compatibility; no confirmation is asked")

	storageShowCmd.Flags().Bool("reveal", false, "Print the secret key")
	storageRotateCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	storageDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	storageUpdateCmd.Flags().Bool("public", false, "Anonymous read of objects (true|false)")
	storageUpdateCmd.Flags().String("max-size", "", "Cap for this bucket (0 removes it)")
	storageQuotaCmd.Flags().String("project-max", "", "Set the project cap (0 = platform default); needs organization admin")
	storageQuotaCmd.Flags().String("org-max", "", "Set the organization cap (0 = platform default); needs organization admin")
	storagePresignCmd.Flags().String("expires", "1h", "Validity: seconds or 15m, 2h, 7d (max 7d)")
	storagePresignCmd.Flags().String("method", "get", "get or put")

	storageCmd.AddCommand(storageCreateCmd, storageListCmd, storageShowCmd, storageUpdateCmd, storageRotateCmd,
		storageQuotaCmd, storagePresignCmd, storageDeleteCmd)
	rootCmd.AddCommand(storageCmd)
}
