package main

import (
	"strings"
	"testing"
)

func TestPortsUDPFlow(t *testing.T) {
	m, done := newPMock(t)
	defer done()

	// tcp (default) and udp with the same container number coexist
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"7777"}, "name": {"game"}}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"7777"}, "name": {"game-udp"}, "udp": {"true"}, "cidr": {"203.0.113.4"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.ports) != 2 {
		t.Fatalf("ports = %v", m.ports)
	}
	tcp, udp := m.ports[0].(map[string]any), m.ports[1].(map[string]any)
	if tcp["protocol"] != "tcp" || udp["protocol"] != "udp" || udp["access"] != "allowlist" {
		t.Fatalf("%v %v", tcp, udp)
	}
	// the same protocol twice is refused, with no request to the server
	n := len(m.calls)
	err := run(t, portsAddCmd, nil, map[string][]string{"container": {"7777"}, "protocol": {"udp"}})
	if err == nil || !strings.Contains(err.Error(), "7777/udp") {
		t.Fatalf("duplicate udp: %v", err)
	}
	for _, c := range m.calls[n:] {
		if c.Method == "PUT" {
			t.Fatal("a duplicate must not reach the server")
		}
	}
	// --protocol validation and contradictions
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"1"}, "protocol": {"sctp"}}); err == nil || !strings.Contains(err.Error(), "tcp or udp") {
		t.Fatalf("sctp: %v", err)
	}
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"1"}, "protocol": {"tcp"}, "udp": {"true"}}); err == nil || !strings.Contains(err.Error(), "contradict") {
		t.Fatalf("--udp with --protocol tcp: %v", err)
	}
	if err := run(t, portsAddCmd, nil, map[string][]string{"container": {"1"}, "protocol": {"UDP"}}); err != nil {
		t.Fatalf("--protocol is case-insensitive: %v", err)
	}
	if len(m.ports) != 3 || m.ports[2].(map[string]any)["protocol"] != "udp" {
		t.Fatalf("%v", m.ports)
	}

	// list: PROTO column, filters
	out, err := runOut(t, portsListCmd, nil, nil)
	if err != nil || !strings.Contains(out, "PROTO") || !strings.Contains(out, "udp") || !strings.Contains(out, "tcp") || !strings.Contains(out, "ports.example:41000") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = runOut(t, portsListCmd, nil, map[string][]string{"udp": {"true"}})
	if err != nil || strings.Contains(out, "game ") && !strings.Contains(out, "game-udp") || strings.Contains(out, "\ttcp\t") {
		t.Fatalf("udp filter: %v\n%s", err, out)
	}
	_ = rootCmd.PersistentFlags().Set("output", "json")
	out, err = runOut(t, portsListCmd, nil, map[string][]string{"protocol": {"tcp"}})
	_ = rootCmd.PersistentFlags().Set("output", "table")
	if err != nil || strings.Contains(out, `"udp"`) || !strings.Contains(out, `"tcp"`) {
		t.Fatalf("tcp json filter: %v\n%s", err, out)
	}
	if _, err := runOut(t, portsListCmd, nil, map[string][]string{"protocol": {"icmp"}}); err == nil {
		t.Fatal("bad filter accepted")
	}

	// access add/remove on the udp port carry the protocol
	if err := run(t, portsAccessAddCmd, []string{"7777"}, map[string][]string{"cidr": {"198.51.100.7"}, "udp": {"true"}}); err != nil {
		t.Fatal(err)
	}
	if c := m.last(); !strings.HasSuffix(c.Path, "/ports/7777/access") || c.Body["protocol"] != "udp" || c.Body["cidr"] != "198.51.100.7/32" {
		t.Fatalf("%+v", c)
	}
	if err := run(t, portsAccessAddCmd, []string{"7777"}, map[string][]string{"cidr": {"198.51.100.8"}}); err != nil {
		t.Fatal(err)
	}
	if c := m.last(); c.Body["protocol"] != nil {
		t.Fatalf("tcp requests stay as before (no protocol key): %+v", c)
	}
	if err := run(t, portsAccessRemoveCmd, []string{"7777", "203.0.113.4/32"}, map[string][]string{"protocol": {"udp"}}); err != nil {
		t.Fatal(err)
	}
	if c := m.last(); c.Method != "DELETE" || !strings.Contains(c.Path, "/ports/7777/access/") || !strings.HasSuffix(c.Path, "?protocol=udp") {
		t.Fatalf("%+v", c)
	}
	// the cidr belongs to the udp port only
	if err := run(t, portsAccessRemoveCmd, []string{"7777", "203.0.113.4/32"}, nil); err == nil {
		t.Fatal("that cidr is not on the tcp port")
	}
	// remove: only the udp twin goes
	if err := run(t, portsRemoveCmd, nil, map[string][]string{"container": {"7777"}, "udp": {"true"}, "yes": {"true"}}); err != nil {
		t.Fatal(err)
	}
	for _, it := range m.ports {
		pm := it.(map[string]any)
		if pm["container_port"] == float64(7777) && pm["protocol"] == "udp" {
			t.Fatalf("udp twin still there: %v", m.ports)
		}
	}
	if err := run(t, portsRemoveCmd, nil, map[string][]string{"container": {"7777"}, "udp": {"true"}, "yes": {"true"}}); err == nil || !strings.Contains(err.Error(), "7777/udp") {
		t.Fatalf("remove missing udp: %v", err)
	}
}
