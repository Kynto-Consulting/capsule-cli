package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ── capsule ports ────────────────────────────────────────────────────────────

var portsCmd = &cobra.Command{
	Use:   "ports",
	Short: "Extra TCP and UDP ports of the project (private by default)",
	Long: `Publishes extra TCP or UDP ports of the project container on the platform host
(direct ports, no tunnel). A port is private (nobody can connect) until you
allow IPs/CIDRs. The same number can be published on tcp and udp at once; add
--udp (or --protocol udp) to every command that targets the UDP one. Changing
the list restarts the container.

UDP has no SNI or Host header, so a UDP service reaches the gateway only through
a capsule:// URI; the direct UDP port is the secondary method.`,
}

// portProto resolves --protocol / --udp to "tcp" or "udp" ("" flags mean tcp).
func portProto(cmd *cobra.Command) (string, error) {
	f := cmd.Flags()
	raw, _ := f.GetString("protocol")
	udp, _ := f.GetBool("udp")
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		raw = "tcp"
	}
	if raw != "tcp" && raw != "udp" {
		return "", fmt.Errorf("--protocol must be tcp or udp (got %q)", raw)
	}
	if udp {
		if f.Changed("protocol") && raw != "udp" {
			return "", fmt.Errorf("--udp and --protocol %s contradict each other", raw)
		}
		raw = "udp"
	}
	return raw, nil
}

// portProtoOf returns the protocol of a listed port ("" from older servers is tcp).
func portProtoOf(p rec) string {
	if s := strings.ToLower(str(p, "protocol")); s != "" {
		return s
	}
	return "tcp"
}

func protoQuery(proto string) string {
	if proto == "udp" {
		return "?protocol=udp"
	}
	return ""
}

func fetchPorts(base string) (rec, []rec, error) {
	var r rec
	if err := apiClient.Get(base+"/ports", &r); err != nil {
		return nil, nil, platErr(err, "extra TCP/UDP ports")
	}
	var ports []rec
	if a, ok := r["ports"].([]any); ok {
		for _, it := range a {
			if m, ok := it.(map[string]any); ok {
				ports = append(ports, m)
			}
		}
	}
	return r, ports, nil
}

func putPorts(base string, ports []rec) error {
	if ports == nil {
		ports = []rec{}
	}
	return platErr(apiClient.Put(base+"/ports", rec{"ports": ports}, nil), "extra TCP/UDP ports")
}

func cidrEntries(cidrs []string, desc string) []rec {
	out := make([]rec, 0, len(cidrs))
	for _, c := range cidrs {
		e := rec{"cidr": c}
		if desc != "" {
			e["description"] = desc
		}
		out = append(out, e)
	}
	return out
}

// accessFromFlags returns normalized CIDRs and whether one of them is public.
func portCIDRs(cmd *cobra.Command) ([]string, bool, error) {
	raw, err := cidrsFromFlags(cmd)
	if err != nil {
		return nil, false, err
	}
	allow, _ := cmd.Flags().GetBool("allow-public")
	cidrs, err := normalizeCIDRs(raw, allow)
	if err != nil {
		return nil, false, err
	}
	pub := false
	for _, c := range cidrs {
		if isPublicCIDR(c) {
			pub = true
		}
	}
	return cidrs, pub, nil
}

// portByContainer finds the tcp port with that container number.
func portByContainer(ports []rec, n string) (int, rec) { return portByKey(ports, n, "tcp") }

// portByKey finds the port with that container number and protocol: the same
// number may exist on tcp and udp at once.
func portByKey(ports []rec, n, proto string) (int, rec) {
	for i, p := range ports {
		if str(p, "container_port") == n && portProtoOf(p) == proto {
			return i, p
		}
	}
	return -1, nil
}

func cidrList(p rec) string {
	var out []string
	if a, ok := p["cidrs"].([]any); ok {
		for _, it := range a {
			if m, ok := it.(map[string]any); ok {
				out = append(out, str(m, "cidr"))
			}
		}
	}
	return strings.Join(out, ",")
}

var portsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List extra ports with their access rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		r, ports, err := fetchPorts(base)
		if err != nil {
			return err
		}
		filter, _ := cmd.Flags().GetString("protocol")
		filter = strings.ToLower(strings.TrimSpace(filter))
		if udp, _ := cmd.Flags().GetBool("udp"); udp {
			filter = "udp"
		}
		if filter != "" && filter != "tcp" && filter != "udp" {
			return fmt.Errorf("--protocol must be tcp or udp (got %q)", filter)
		}
		if filter != "" {
			var kept []rec
			for _, p := range ports {
				if portProtoOf(p) == filter {
					kept = append(kept, p)
				}
			}
			ports = kept
			if a, ok := r["ports"].([]any); ok {
				out := make([]any, 0, len(a))
				for _, it := range a {
					if m, ok := it.(map[string]any); ok && portProtoOf(m) == filter {
						out = append(out, it)
					}
				}
				r["ports"] = out
			}
		}
		if wantJSON(cmd) {
			return printJSON(r)
		}
		host := str(r, "address_host")
		if len(ports) == 0 {
			if filter != "" {
				fmt.Printf("No extra %s ports. Add one with: capsule ports add --container 8080 --protocol %s --my-ip\n", strings.ToUpper(filter), filter)
			} else {
				fmt.Println("No extra ports. Add one with: capsule ports add --container 8080 --my-ip (or --udp for a UDP port)")
			}
			return nil
		}
		w := newTW()
		fmt.Fprintln(w, "NAME\tPROTO\tCONTAINER\tADDRESS\tACCESS\tALLOWED")
		for _, p := range ports {
			addr := str(p, "host_port")
			if host != "" && addr != "" {
				addr = host + ":" + addr
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", str(p, "name"), portProtoOf(p), str(p, "container_port"), addr, str(p, "access"), cidrList(p))
		}
		return w.Flush()
	},
}

var portsAddCmd = &cobra.Command{
	Use:   "add --container PORT",
	Short: "Publish an extra TCP or UDP port (private unless you allow IPs)",
	Example: `  capsule ports add --container 5432 --name pg --my-ip
  capsule ports add --container 8080 --cidr 203.0.113.0/24 --cidr 198.51.100.7
  capsule ports add --container 9000                 # private: nobody can connect yet
  capsule ports add --container 7777 --udp --name game --my-ip   # UDP (same as --protocol udp)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		cp, _ := f.GetInt("container")
		if cp < 1 || cp > 65535 {
			return fmt.Errorf("--container must be a port between 1 and 65535")
		}
		proto, err := portProto(cmd)
		if err != nil {
			return err
		}
		name, _ := f.GetString("name")
		cidrs, pub, err := portCIDRs(cmd)
		if err != nil {
			return err
		}
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		if pub {
			if err := abortUnless(cmd, fmt.Sprintf("Port %d/%s will be reachable from the WHOLE INTERNET. Continue?", cp, proto)); err != nil {
				return err
			}
		}
		_, ports, err := fetchPorts(base)
		if err != nil {
			return err
		}
		if i, _ := portByKey(ports, fmt.Sprint(cp), proto); i >= 0 {
			return fmt.Errorf("container port %d/%s is already published; use `capsule ports access add`", cp, proto)
		}
		if name == "" {
			name = fmt.Sprintf("port-%d", cp)
		}
		entry := rec{"name": name, "container_port": cp, "protocol": proto, "access": "private", "cidrs": []rec{}}
		desc, _ := f.GetString("desc")
		switch {
		case pub:
			entry["access"] = "public"
		case len(cidrs) > 0:
			entry["access"] = "allowlist"
			entry["cidrs"] = cidrEntries(cidrs, desc)
		}
		if err := putPorts(base, append(ports, entry)); err != nil {
			return err
		}
		fmt.Printf("Port %d/%s published as %s (the container restarts). See the address with: capsule ports list\n", cp, proto, entry["access"])
		return nil
	},
}

var portsRemoveCmd = &cobra.Command{
	Use:   "remove --container PORT",
	Short: "Stop publishing an extra port",
	RunE: func(cmd *cobra.Command, args []string) error {
		cp, _ := cmd.Flags().GetInt("container")
		if cp < 1 {
			return fmt.Errorf("--container is required")
		}
		proto, err := portProto(cmd)
		if err != nil {
			return err
		}
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		_, ports, err := fetchPorts(base)
		if err != nil {
			return err
		}
		i, _ := portByKey(ports, fmt.Sprint(cp), proto)
		if i < 0 {
			return fmt.Errorf("container port %d/%s is not published", cp, proto)
		}
		if err := abortUnless(cmd, fmt.Sprintf("Unpublish port %d/%s? The container restarts.", cp, proto)); err != nil {
			return err
		}
		if err := putPorts(base, append(ports[:i], ports[i+1:]...)); err != nil {
			return err
		}
		fmt.Printf("Port %d/%s removed.\n", cp, proto)
		return nil
	},
}

var portsAccessCmd = &cobra.Command{
	Use:   "access",
	Short: "Manage who can reach a published port",
}

var portsAccessAddCmd = &cobra.Command{
	Use:   "add CONTAINER_PORT",
	Short: "Allow IPs/CIDRs to reach a port",
	Args:  cobra.ExactArgs(1),
	Example: `  capsule ports access add 5432 --my-ip --desc "laptop"
  capsule ports access add 8080 --cidr 203.0.113.0/24`,
	RunE: func(cmd *cobra.Command, args []string) error {
		proto, err := portProto(cmd)
		if err != nil {
			return err
		}
		cidrs, pub, err := portCIDRs(cmd)
		if err != nil {
			return err
		}
		if len(cidrs) == 0 {
			return fmt.Errorf("use --cidr <ip/cidr> or --my-ip")
		}
		if pub {
			if err := abortUnless(cmd, fmt.Sprintf("Port %s/%s will be reachable from the WHOLE INTERNET. Continue?", args[0], proto)); err != nil {
				return err
			}
		}
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		desc, _ := cmd.Flags().GetString("desc")
		for _, c := range cidrs {
			body := rec{"cidr": c}
			if desc != "" {
				body["description"] = desc
			}
			if isPublicCIDR(c) {
				body["allow_public"] = true
			}
			if proto == "udp" {
				body["protocol"] = "udp"
			}
			if err := apiClient.Post(base+"/ports/"+args[0]+"/access", body, nil); err != nil {
				return platErr(err, "extra TCP/UDP ports")
			}
			fmt.Printf("Allowed %s on port %s/%s\n", c, args[0], proto)
		}
		return nil
	},
}

var portsAccessRemoveCmd = &cobra.Command{
	Use:   "remove CONTAINER_PORT RULE_ID|CIDR",
	Short: "Remove an access rule (short rule ID or the CIDR)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		proto, err := portProto(cmd)
		if err != nil {
			return err
		}
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		_, ports, err := fetchPorts(base)
		if err != nil {
			return err
		}
		_, p := portByKey(ports, args[0], proto)
		if p == nil {
			return fmt.Errorf("container port %s/%s is not published", args[0], proto)
		}
		var rules []rec
		if a, ok := p["cidrs"].([]any); ok {
			for _, it := range a {
				if m, ok := it.(map[string]any); ok {
					rules = append(rules, m)
				}
			}
		}
		ref := args[1]
		var id string
		if strings.ContainsAny(ref, "./:") {
			c, err := normalizeCIDR(ref)
			if err != nil {
				return err
			}
			for _, r := range rules {
				if str(r, "cidr") == c {
					id = str(r, "id")
				}
			}
			if id == "" {
				return fmt.Errorf("no rule for %s on port %s", c, args[0])
			}
		} else {
			r, err := pickByPrefix(rules, ref, "rule", "id")
			if err != nil {
				return err
			}
			id = str(r, "id")
		}
		if err := apiClient.Delete(base + "/ports/" + args[0] + "/access/" + id + protoQuery(proto)); err != nil {
			return platErr(err, "extra TCP/UDP ports")
		}
		fmt.Println("Rule removed.")
		return nil
	},
}

// ── capsule backups ──────────────────────────────────────────────────────────

var backupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "Scheduled backups of the project (databases and volumes)",
}

var backupSchedules = map[string]bool{"hourly": true, "6h": true, "12h": true, "daily": true, "weekly": true}

var backupsShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the backup policy, recent runs and the next run",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		v, err := getRaw(base + "/backups")
		if err != nil {
			return platErr(err, "backups")
		}
		if wantJSON(cmd) {
			return printJSON(v)
		}
		m, _ := v.(map[string]any)
		inc, _ := m["include"].(map[string]any)
		fmt.Printf("enabled:        %s\nschedule:       %s\nretention days: %s\ndatabases:      %s\nvolumes:        %s\nnext run:       %s\n",
			str(m, "enabled"), str(m, "schedule"), str(m, "retention_days"), str(inc, "databases"), str(inc, "volumes"), str(m, "next_run"))
		if runs := asList(m["last_runs"]); len(runs) > 0 {
			fmt.Println("\nlast runs:")
			w := newTW()
			fmt.Fprintln(w, "  WHEN\tSTATUS\tSIZE\tSNAPSHOT")
			for _, r := range runs {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", str(r, "started_at", "created_at"), str(r, "status"), str(r, "size_bytes", "size"), short8(str(r, "snapshot_id", "id")))
			}
			w.Flush()
		}
		return nil
	},
}

var backupsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Change the backup policy (only the flags you pass change)",
	Example: `  capsule backups set --enabled=true --schedule daily --retention-days 14 --databases --volumes
  capsule backups set --schedule 6h`,
	RunE: func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		var cur rec
		if err := apiClient.Get(base+"/backups", &cur); err != nil {
			return platErr(err, "backups")
		}
		body := rec{"enabled": cur["enabled"], "schedule": cur["schedule"], "retention_days": cur["retention_days"]}
		inc := rec{"databases": true, "volumes": true}
		if m, ok := cur["include"].(map[string]any); ok {
			inc = m
		}
		body["include"] = inc
		changed := false
		if f.Changed("enabled") {
			body["enabled"], _ = f.GetBool("enabled")
			changed = true
		}
		if f.Changed("schedule") {
			s, _ := f.GetString("schedule")
			if !backupSchedules[s] {
				return fmt.Errorf("invalid --schedule %q (hourly, 6h, 12h, daily, weekly)", s)
			}
			body["schedule"] = s
			changed = true
		}
		if f.Changed("retention-days") {
			d, _ := f.GetInt("retention-days")
			if d < 1 {
				return fmt.Errorf("--retention-days must be at least 1")
			}
			body["retention_days"] = d
			changed = true
		}
		for _, k := range []string{"databases", "volumes"} {
			if f.Changed(k) {
				inc[k], _ = f.GetBool(k)
				changed = true
			}
		}
		if !changed {
			return fmt.Errorf("nothing to change: pass --enabled, --schedule, --retention-days, --databases or --volumes")
		}
		if err := apiClient.Put(base+"/backups", body, nil); err != nil {
			return platErr(err, "backups")
		}
		fmt.Println("Backup policy updated.")
		return nil
	},
}

var backupsRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a backup now",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		var r rec
		if err := apiClient.Post(base+"/backups/run", rec{}, &r); err != nil {
			return platErr(err, "backups")
		}
		fmt.Println("Backup started.", str(r, "snapshot_id", "id"))
		return nil
	},
}

var backupsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List snapshots",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		v, err := getRaw(base + "/backups/snapshots")
		if err != nil {
			return platErr(err, "backups")
		}
		if wantJSON(cmd) {
			return printJSON(v)
		}
		w := newTW()
		fmt.Fprintln(w, "SNAPSHOT\tCREATED\tSIZE\tCONTENT")
		for _, s := range asList(v, "snapshots") {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", short8(str(s, "id", "snapshot_id")), str(s, "created_at"), str(s, "size_bytes", "size"), str(s, "include", "kind"))
		}
		return w.Flush()
	},
}

var backupsRestoreCmd = &cobra.Command{
	Use:   "restore SNAPSHOT_ID",
	Short: "Restore a snapshot (overwrites current data)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := projBase(cmd)
		if err != nil {
			return err
		}
		id := args[0]
		if len(id) < 36 {
			v, err := getRaw(base + "/backups/snapshots")
			if err != nil {
				return platErr(err, "backups")
			}
			r, err := pickByPrefix(asList(v, "snapshots"), id, "snapshot", "id", "snapshot_id")
			if err != nil {
				return err
			}
			id = str(r, "id", "snapshot_id")
		}
		target, _ := cmd.Flags().GetString("target")
		label := "Restore snapshot " + short8(id) + "? This OVERWRITES the current data of the project."
		if target != "" {
			label = "Restore snapshot " + short8(id) + " into " + target + "?"
		}
		if err := abortUnless(cmd, label); err != nil {
			return err
		}
		body := rec{"snapshot_id": id}
		if target != "" {
			body["target"] = target
		}
		if err := apiClient.Post(base+"/backups/restore", body, nil); err != nil {
			return platErr(err, "backups")
		}
		fmt.Println("Restore started for snapshot", short8(id))
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{portsListCmd, portsAddCmd, portsRemoveCmd, portsAccessAddCmd, portsAccessRemoveCmd,
		backupsShowCmd, backupsSetCmd, backupsRunCmd, backupsListCmd, backupsRestoreCmd} {
		addNetFlags(c)
	}
	for _, c := range []*cobra.Command{portsAddCmd, portsAccessAddCmd} {
		c.Flags().StringSlice("cidr", nil, "IP or CIDR (a bare IP becomes /32); repeatable")
		c.Flags().Bool("my-ip", false, "Usa tu IP pública actual (/32 o /128; IPv4 o IPv6)")
		c.Flags().Bool("allow-public", false, "Permite 0.0.0.0/0 o ::/0 (pide confirmación)")
		c.Flags().String("desc", "", "Description of the rule")
		c.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	}
	for _, c := range []*cobra.Command{portsAddCmd, portsRemoveCmd, portsAccessAddCmd, portsAccessRemoveCmd, portsListCmd} {
		c.Flags().String("protocol", "", "tcp or udp (default tcp; on list, filters by protocol)")
		c.Flags().Bool("udp", false, "Shorthand for --protocol udp")
	}
	portsAddCmd.Flags().Int("container", 0, "Container port to publish")
	portsAddCmd.Flags().String("name", "", "Port name")
	portsRemoveCmd.Flags().Int("container", 0, "Container port to unpublish")
	portsRemoveCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	portsAccessCmd.AddCommand(portsAccessAddCmd, portsAccessRemoveCmd)
	portsCmd.AddCommand(portsListCmd, portsAddCmd, portsRemoveCmd, portsAccessCmd)
	rootCmd.AddCommand(portsCmd)

	backupsSetCmd.Flags().Bool("enabled", true, "Enable or disable scheduled backups")
	backupsSetCmd.Flags().String("schedule", "", "hourly, 6h, 12h, daily or weekly")
	backupsSetCmd.Flags().Int("retention-days", 0, "Days to keep snapshots")
	backupsSetCmd.Flags().Bool("databases", true, "Include databases")
	backupsSetCmd.Flags().Bool("volumes", true, "Include volumes")
	backupsRestoreCmd.Flags().String("target", "", "Restore into another target instead of in place")
	backupsRestoreCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	backupsCmd.AddCommand(backupsShowCmd, backupsSetCmd, backupsRunCmd, backupsListCmd, backupsRestoreCmd)
	rootCmd.AddCommand(backupsCmd)
}
