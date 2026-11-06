package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// capsule vpn …: VPN Connector (docs/vpn-connector.md). One tunnel per project. Credentials are read from stdin and
// stored as an encrypted project secret; they are never accepted in argv and never printed back.

func vpnBase(cmd *cobra.Command) (string, error) {
	orgID, projectID, err := resolveOrgProject(cmd)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/v1/orgs/%s/projects/%s/vpn", orgID, projectID), nil
}

type vpnTunnel struct {
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	Host         string   `json:"host"`
	CIDRs        []string `json:"cidrs"`
	CertPin      string   `json:"cert_pin"`
	Enabled      bool     `json:"enabled"`
	Status       string   `json:"status"`
	LastError    string   `json:"last_error"`
	LastHS       *string  `json:"last_handshake_at"`
	MaxConns     int      `json:"max_conns"`
	BandwidthBps int64    `json:"bandwidth_bps"`
}

var vpnCmd = &cobra.Command{Use: "vpn", Short: "VPN Connector: a tunnel to a customer network for project functions"}

var vpnCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create the project's tunnel (credentials from stdin with --secret-stdin)",
	Long: `Create the tunnel of the project. Only the declared --cidr networks are routed; the host's DNS and routes are not touched.
The credentials are a JSON document read from stdin (never from arguments):
  wireguard  {"private_key","peer_public_key","preshared_key"?,"address":"10.99.0.2/32"}
  fortigate  {"username","password"}      (--cert-pin <sha256 of the remote certificate> is recommended)
  openvpn    {"username","password"}
  ipsec      {"psk"} or {"username","password"}`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := vpnBase(cmd)
		if err != nil {
			return err
		}
		provider, _ := cmd.Flags().GetString("provider")
		host, _ := cmd.Flags().GetString("host")
		cidrs, _ := cmd.Flags().GetStringSlice("cidr")
		pin, _ := cmd.Flags().GetString("cert-pin")
		fromStdin, _ := cmd.Flags().GetBool("secret-stdin")
		maxConns, _ := cmd.Flags().GetInt("max-conns")
		bw, _ := cmd.Flags().GetInt64("bandwidth")
		if !fromStdin {
			return fmt.Errorf("--secret-stdin is required: credentials are never passed as arguments")
		}
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return err
		}
		body := map[string]any{"name": args[0], "provider": provider, "host": host, "cidrs": cidrs, "cert_pin": pin,
			"secret": strings.TrimSpace(string(b)), "max_conns": maxConns, "bandwidth_bps": bw}
		var resp struct {
			Tunnel  vpnTunnel `json:"tunnel"`
			Warning string    `json:"warning"`
		}
		if err := apiClient.Post(base, body, &resp); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(resp)
		}
		fmt.Printf("Tunnel %s created (%s -> %s, networks %s). Credentials stored as an encrypted project secret.\n",
			resp.Tunnel.Name, resp.Tunnel.Provider, resp.Tunnel.Host, strings.Join(resp.Tunnel.CIDRs, ","))
		if resp.Warning != "" {
			fmt.Println("Warning:", resp.Warning)
		}
		return nil
	},
}

var vpnStatusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show tunnel health",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := vpnBase(cmd)
		if err != nil {
			return err
		}
		var list []vpnTunnel
		if len(args) == 1 {
			var one vpnTunnel
			if err := apiClient.Get(base+"/"+args[0], &one); err != nil {
				return err
			}
			list = []vpnTunnel{one}
		} else if err := apiClient.Get(base, &list); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(list)
		}
		if len(list) == 0 {
			fmt.Println("No tunnel in this project.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tPROVIDER\tSTATUS\tENABLED\tNETWORKS\tHANDSHAKE\tERROR")
		for _, t := range list {
			hs := "-"
			if t.LastHS != nil {
				hs = *t.LastHS
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\t%s\t%s\n", t.Name, t.Provider, t.Status, t.Enabled, strings.Join(t.CIDRs, ","), hs, t.LastError)
		}
		return tw.Flush()
	},
}

var vpnLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Audit trail of the tunnel: connections, reconnections, runs, denied targets, kill switch",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := vpnBase(cmd)
		if err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		var ev []struct {
			At      string `json:"at"`
			Kind    string `json:"kind"`
			Message string `json:"message"`
			Actor   string `json:"actor"`
		}
		if err := apiClient.Get(fmt.Sprintf("%s/%s/events?limit=%d", base, args[0], limit), &ev); err != nil {
			return err
		}
		if wantJSON(cmd) {
			return printJSON(ev)
		}
		for i := len(ev) - 1; i >= 0; i-- {
			fmt.Printf("%s  %-14s %-16s %s\n", ev[i].At, ev[i].Kind, ev[i].Actor, ev[i].Message)
		}
		return nil
	},
}

func vpnSwitch(enabled bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		base, err := vpnBase(cmd)
		if err != nil {
			return err
		}
		var t vpnTunnel
		if err := apiClient.Put(base+"/"+args[0]+"/enabled", map[string]bool{"enabled": enabled}, &t); err != nil {
			return err
		}
		if enabled {
			fmt.Printf("Tunnel %s enabled.\n", t.Name)
		} else {
			fmt.Printf("Tunnel %s OFF: connections dropped, credentials no longer handed out, running functions cancelled.\n", t.Name)
		}
		return nil
	}
}

var vpnOffCmd = &cobra.Command{Use: "off <name>", Short: "Kill switch: shut the tunnel down at once", Args: cobra.ExactArgs(1), RunE: vpnSwitch(false)}
var vpnOnCmd = &cobra.Command{Use: "on <name>", Short: "Enable a tunnel that was switched off", Args: cobra.ExactArgs(1), RunE: vpnSwitch(true)}

var vpnDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete the tunnel (and stop what uses it)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := vpnBase(cmd)
		if err != nil {
			return err
		}
		if err := apiClient.Delete(base + "/" + args[0]); err != nil {
			return err
		}
		fmt.Println("Tunnel deleted")
		return nil
	},
}

func init() {
	all := []*cobra.Command{vpnCreateCmd, vpnStatusCmd, vpnLogsCmd, vpnOffCmd, vpnOnCmd, vpnDeleteCmd}
	for _, c := range all {
		c.Flags().String("org", "", "Org ID (overrides .capsule.json)")
		c.Flags().String("project", "", "Project ID (overrides .capsule.json)")
	}
	vpnCreateCmd.Flags().String("provider", "", "wireguard | fortigate | ipsec | openvpn")
	vpnCreateCmd.Flags().String("host", "", "remote gateway host:port")
	vpnCreateCmd.Flags().StringSlice("cidr", nil, "customer network to route (repeatable or comma separated); nothing else is routed")
	vpnCreateCmd.Flags().String("cert-pin", "", "SHA-256 of the remote certificate (recommended)")
	vpnCreateCmd.Flags().Bool("secret-stdin", false, "read the credentials JSON from stdin")
	vpnCreateCmd.Flags().Int("max-conns", 0, "simultaneous connections through the tunnel (default 4)")
	vpnCreateCmd.Flags().Int64("bandwidth", 0, "bytes per second ceiling for the tunnel (default 8 MiB/s)")
	_ = vpnCreateCmd.MarkFlagRequired("provider")
	_ = vpnCreateCmd.MarkFlagRequired("host")
	vpnLogsCmd.Flags().Int("limit", 100, "events to show")
	vpnCmd.AddCommand(all...)
	rootCmd.AddCommand(vpnCmd)
}
