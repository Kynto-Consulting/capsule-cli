package main

import (
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Shared helpers for `capsule db access` and `capsule network`.

// normalizeCIDR accepts an IPv4/IPv6 address or CIDR and returns the canonical
// form (the same rules as the backend, shared test table testdata/cidr_cases.json):
// bare address -> /32 or /128, host bits cleared, IPv6 compressed lowercase,
// IPv4-mapped (::ffff:a.b.c.d) treated as IPv4, zones rejected, IPv4 prefixes
// shorter than /8 and IPv6 shorter than /32 rejected (except the /0 of each family,
// which callers gate with --allow-public).
func normalizeCIDR(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("CIDR vacío")
	}
	if strings.Contains(s, "%") {
		return "", fmt.Errorf("no se admiten zonas (%%) en %q", s)
	}
	var p netip.Prefix
	if strings.Contains(s, "/") {
		var err error
		if p, err = netip.ParsePrefix(s); err != nil {
			return "", fmt.Errorf("CIDR inválido %q", s)
		}
	} else {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return "", fmt.Errorf("IP o CIDR inválido %q", s)
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if p.Addr().Is4In6() {
		if p.Bits() < 96 {
			return "", fmt.Errorf("un prefijo IPv4-mapeado debe ser /96 o más largo (%q)", s)
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	if p.Bits() == 0 {
		if p.Addr().Is4() {
			return "0.0.0.0/0", nil
		}
		return "::/0", nil
	}
	if p.Addr().Is4() && p.Bits() < 8 {
		return "", fmt.Errorf("el prefijo IPv4 debe ser /8 o más largo (%q)", s)
	}
	if p.Addr().Is6() && p.Bits() < 32 {
		return "", fmt.Errorf("el prefijo IPv6 debe ser /32 o más largo (%q)", s)
	}
	return p.Masked().String(), nil
}

// isPublicCIDR reports whether the CIDR opens access to the whole internet.
func isPublicCIDR(c string) bool {
	p, err := netip.ParsePrefix(c)
	return err == nil && p.Bits() == 0
}

// normalizeCIDRs normalizes and de-duplicates. Public ranges (/0) are
// rejected unless allowPublic is set.
func normalizeCIDRs(in []string, allowPublic bool) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		c, err := normalizeCIDR(raw)
		if err != nil {
			return nil, err
		}
		if isPublicCIDR(c) && !allowPublic {
			return nil, fmt.Errorf("%s opens access to the whole internet; pass --allow-public to do it explicitly", c)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

func fetchMyIP() (string, error) {
	var r struct {
		IP string `json:"ip"`
	}
	if err := apiClient.Get("/api/v1/network/my-ip", &r); err != nil {
		return "", friendlyNetErr(err)
	}
	if r.IP == "" {
		return "", fmt.Errorf("the API did not return your IP")
	}
	return r.IP, nil
}

func ipFamilyLabel(ip string) string {
	if a, err := netip.ParseAddr(ip); err == nil && a.Unmap().Is6() {
		return "IPv6"
	}
	return "IPv4"
}

func otherFamilyLabel(ip string) string {
	if ipFamilyLabel(ip) == "IPv6" {
		return "IPv4"
	}
	return "IPv6"
}

// cidrsFromFlags gathers --cidr (repeatable) and --my-ip into raw values.
func cidrsFromFlags(cmd *cobra.Command) ([]string, error) {
	cidrs, _ := cmd.Flags().GetStringSlice("cidr")
	if my, _ := cmd.Flags().GetBool("my-ip"); my {
		ip, err := fetchMyIP()
		if err != nil {
			return nil, err
		}
		fmt.Printf("Your public IP: %s\n", ip)
		cidrs = append(cidrs, ip)
	}
	return cidrs, nil
}

// friendlyNetErr turns the backend's status codes into actionable messages.
func friendlyNetErr(err error) error {
	if err == nil {
		return nil
	}
	m := err.Error()
	switch {
	case strings.Contains(m, "API error 403"):
		return fmt.Errorf("%w\n  This token cannot manage network access. Tokens with scope \"deploy\" are not allowed; use a full-access token or run `capsule login`", err)
	case strings.Contains(m, "API error 501"):
		return fmt.Errorf("%w\n  Network access control is not enabled on this Capsule backend (APP_SECURITY_GROUP_ID is not configured)", err)
	case strings.Contains(m, "API error 422"):
		return fmt.Errorf("%w\n  The server rejected the CIDR list; check the values", err)
	}
	return err
}

// confirmOrYes asks for confirmation unless --yes was passed.
func confirmOrYes(cmd *cobra.Command, label string) bool {
	if y, _ := cmd.Flags().GetBool("yes"); y {
		return true
	}
	return confirm(label, false)
}

func addNetFlags(c *cobra.Command) {
	c.Flags().String("org", "", "Org ID (overrides .capsule.json)")
	c.Flags().String("project", "", "Project ID (overrides .capsule.json)")
}

func stderrf(format string, a ...any) { fmt.Fprintf(os.Stderr, format, a...) }
