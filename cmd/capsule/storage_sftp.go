package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	gw "github.com/kynto-consulting/capsule/cli/pkg/gatewayclient"
)

// SFTP access to a bucket. The server lives inside the Capsule backend and has no public port: it is reached through
// the gateway as the project's "sftp" service (capsule://<project>/sftp), exactly like `capsule ssh`.

type sftpInfo struct {
	User         string `json:"user"`
	GatewayURI   string `json:"gateway_uri"`
	InternalAddr string `json:"internal_addr"`
	Fingerprint  string `json:"host_key_fingerprint"`
}

type sshKeyItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Fingerprint string  `json:"fingerprint"`
	CreatedAt   string  `json:"created_at"`
	LastUsedAt  *string `json:"last_used_at"`
}

// sftpClientArgs are the arguments of the OpenSSH sftp/scp clients for a bucket through the gateway (without the
// executable): the gateway is the ProxyCommand, HostKeyAlias keeps the host key per project, and the user is the bucket.
func sftpClientArgs(u *gw.URI, proxy, user string, extra []string) []string {
	alias := sshAlias(u)
	a := []string{"-o", "ProxyCommand=" + proxy, "-o", "HostKeyAlias=" + alias}
	a = append(a, extra...)
	return append(a, user+"@"+alias)
}

// sftpGuide is the connection guide printed by `capsule storage sftp --print`.
func sftpGuide(bucket string, si *sftpInfo, u *gw.URI, exe, raw, zone, goos string) string {
	proxy := proxyCommand(exe, raw, zone, goos)
	alias := sshAlias(u)
	var b strings.Builder
	fmt.Fprintf(&b, "SFTP of bucket %s\n\n", bucket)
	fmt.Fprintf(&b, "  Service     %s   (through the Capsule gateway; there is no public SFTP port)\n", si.GatewayURI)
	fmt.Fprintf(&b, "  User        %s\n", si.User)
	fmt.Fprintf(&b, "  Password    the bucket's secret key (capsule storage show %s --reveal), or an SSH key you registered:\n", bucket)
	fmt.Fprintf(&b, "              capsule storage ssh-keys add %s --file ~/.ssh/id_ed25519.pub\n", bucket)
	if si.Fingerprint != "" {
		fmt.Fprintf(&b, "  Host key    %s\n", si.Fingerprint)
	}
	fmt.Fprintf(&b, "  Apps on the same server: host.docker.internal:%s (same user and password)\n\n", portOf(si.InternalAddr))
	b.WriteString("Command line (needs the native capsule binary and OpenSSH):\n\n")
	fmt.Fprintf(&b, "  capsule storage sftp %s\n", bucket)
	fmt.Fprintf(&b, "  sftp -o ProxyCommand=%s -o HostKeyAlias=%s %s@%s\n", proxy, alias, si.User, alias)
	fmt.Fprintf(&b, "  scp  -o ProxyCommand=%s -o HostKeyAlias=%s ./file.bin %s@%s:/folder/file.bin\n\n", proxy, alias, si.User, alias)
	b.WriteString("~/.ssh/config (then: sftp " + alias + " ; scp file " + alias + ":/folder/):\n\n")
	b.WriteString(indent(sshConfigBlock(u, proxy, si.User), "  "))
	b.WriteString("\nWinSCP: New Site > File protocol SFTP > Host name " + alias + ", Port 22, User name " + si.User + ", Password = the secret key.\n")
	b.WriteString("  Advanced > Connection > Proxy: type \"Local\", Telnet/Local proxy command:\n")
	fmt.Fprintf(&b, "    %s\n", localProxy(exe, raw, zone))
	b.WriteString("\nFileZilla (no proxy-command support): open a local tunnel and connect to it.\n")
	fmt.Fprintf(&b, "  capsule tunnel %s --listen 127.0.0.1:2222\n", strings.TrimPrefix(raw, "capsule://"))
	fmt.Fprintf(&b, "  FileZilla > Site Manager > Protocol SFTP, Host 127.0.0.1, Port 2222, Logon type Normal, User %s, Password = the secret key.\n", si.User)
	return b.String()
}

func localProxy(exe, raw, zone string) string {
	q := func(s string) string { return `"` + s + `"` }
	s := q(exe) + " connect --stdio"
	if zone != "" {
		s += " --zone " + zone
	}
	return s + " " + raw
}

func portOf(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[i+1:]
	}
	return "2222"
}

func indent(s, p string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = p + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

var storageSFTPCmd = &cobra.Command{
	Use:   "sftp <bucket> [-- sftp options...]",
	Short: "Open the bucket with sftp through the gateway (or print how to connect scp, WinSCP, FileZilla)",
	Long: `Opens an SFTP session on a bucket: the bucket is a tree of folders (key prefixes separated by "/").
The connection goes through the Capsule gateway as the project's sftp service; nothing is opened to the internet.

  capsule storage sftp files                 run sftp (user = the bucket, password = its secret key, or your SSH key)
  capsule storage sftp files -- -b cmds.txt  options after -- go to sftp
  capsule storage sftp files --print         connection guide: sftp, scp, ~/.ssh/config, WinSCP, FileZilla

Needs the native capsule binary and OpenSSH on the PATH (--print works everywhere).`,
	Args: func(cmd *cobra.Command, args []string) error {
		n := len(args)
		if d := cmd.ArgsLenAtDash(); d >= 0 {
			n = d
		}
		if n != 1 {
			return usageErr("expected exactly one bucket")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		var extra []string
		if d := cmd.ArgsLenAtDash(); d >= 0 {
			extra = args[d:]
			args = args[:d]
		}
		orgID, projectID, b, _, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		var full storageItem
		if err := apiClient.Get(storageBase(orgID, projectID)+"/"+b.ID, &full); err != nil {
			return err
		}
		if full.SFTP == nil {
			return fmt.Errorf("SFTP is not enabled on this Capsule server")
		}
		zone, _ := cmd.Flags().GetString("zone")
		raw, u, err := parseTarget(full.SFTP.GatewayURI, zone)
		if err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot locate the capsule executable: %v", err)
		}
		if print, _ := cmd.Flags().GetBool("print"); print {
			fmt.Print(sftpGuide(full.Bucket, full.SFTP, u, filepath.ToSlash(exe), raw, zone, runtime.GOOS))
			return nil
		}
		if err := requireNative("sftp"); err != nil {
			return err
		}
		bin, err := exec.LookPath("sftp")
		if err != nil {
			return fmt.Errorf("sftp (OpenSSH) not found in the PATH: install OpenSSH or use --print for WinSCP and FileZilla")
		}
		c := exec.Command(bin, sftpClientArgs(u, proxyCommand(exe, raw, zone, runtime.GOOS), full.SFTP.User, extra)...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			if xe, ok := err.(*exec.ExitError); ok {
				return &exitError{code: xe.ExitCode(), msg: "", err: err}
			}
			return fmt.Errorf("could not run sftp: %v", err)
		}
		return nil
	},
}

var storageSSHKeysCmd = &cobra.Command{
	Use:   "ssh-keys",
	Short: "SSH public keys that may open a bucket over SFTP",
	Long: `Besides the secret key (as password), a bucket accepts SSH public keys for SFTP.

  capsule storage ssh-keys add files --file ~/.ssh/id_ed25519.pub [--name laptop]
  capsule storage ssh-keys list files
  capsule storage ssh-keys rm files laptop`,
}

func readPublicKey(file, inline string) (string, error) {
	switch {
	case inline != "":
		return inline, nil
	case file == "-":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<10))
		return string(b), err
	case file != "":
		b, err := os.ReadFile(file)
		return string(b), err
	}
	home, _ := os.UserHomeDir()
	for _, n := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		if b, err := os.ReadFile(filepath.Join(home, ".ssh", n)); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("no public key found: pass --file <key.pub> (or --key \"ssh-ed25519 AAAA... comment\")")
}

var storageSSHKeysAddCmd = &cobra.Command{
	Use:   "add <bucket>",
	Short: "Register an SSH public key for the bucket",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, _, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		file, _ := cmd.Flags().GetString("file")
		inline, _ := cmd.Flags().GetString("key")
		name, _ := cmd.Flags().GetString("name")
		pub, err := readPublicKey(file, inline)
		if err != nil {
			return err
		}
		var k sshKeyItem
		if err := apiClient.Post(storageBase(orgID, projectID)+"/"+b.ID+"/ssh-keys", map[string]string{"name": name, "public_key": strings.TrimSpace(pub)}, &k); err != nil {
			return err
		}
		fmt.Printf("Key %q (%s) registered for %s. Log in with: capsule storage sftp %s\n", k.Name, k.Fingerprint, b.Name, b.Name)
		return nil
	},
}

var storageSSHKeysListCmd = &cobra.Command{
	Use:     "list <bucket>",
	Aliases: []string{"ls"},
	Short:   "List the SSH keys registered for the bucket",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, _, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		var resp struct {
			Data []sshKeyItem `json:"data"`
		}
		if err := apiClient.Get(storageBase(orgID, projectID)+"/"+b.ID+"/ssh-keys", &resp); err != nil {
			return err
		}
		if len(resp.Data) == 0 {
			fmt.Println("No SSH keys: the bucket's secret key is the SFTP password. Add one with: capsule storage ssh-keys add " + b.Name)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tFINGERPRINT\tADDED\tLAST USED")
		for _, k := range resp.Data {
			last := "never"
			if k.LastUsedAt != nil {
				last = *k.LastUsedAt
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.Name, k.Fingerprint, k.CreatedAt, last)
		}
		return w.Flush()
	},
}

var storageSSHKeysRmCmd = &cobra.Command{
	Use:     "rm <bucket> <name|fingerprint|id>",
	Aliases: []string{"remove", "delete"},
	Short:   "Remove an SSH key (it stops working at once)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, _, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		if err := apiClient.Delete(storageBase(orgID, projectID) + "/" + b.ID + "/ssh-keys/" + args[1]); err != nil {
			return err
		}
		fmt.Printf("Key %s removed from %s.\n", args[1], b.Name)
		return nil
	},
}

func init() {
	storageSFTPCmd.Flags().Bool("print", false, "print the connection guide (sftp, scp, ssh config, WinSCP, FileZilla) instead of connecting")
	storageSFTPCmd.Flags().String("zone", "", "gateway zone (default "+gw.ZoneEnv+" or "+gw.DefaultZone+")")
	storageSSHKeysAddCmd.Flags().String("file", "", "public key file ('-' reads stdin; default ~/.ssh/id_ed25519.pub, id_ecdsa.pub, id_rsa.pub)")
	storageSSHKeysAddCmd.Flags().String("key", "", "the public key itself (\"ssh-ed25519 AAAA... comment\")")
	storageSSHKeysAddCmd.Flags().String("name", "", "label for the key (default: the key comment)")
	storageSSHKeysCmd.AddCommand(storageSSHKeysAddCmd, storageSSHKeysListCmd, storageSSHKeysRmCmd)
	storageCmd.AddCommand(storageSFTPCmd, storageSSHKeysCmd)
}
