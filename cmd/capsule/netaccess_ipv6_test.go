package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// Shares the table with the backend and the dashboard (see testdata/cidr_cases.json).
func TestNormalizeCIDRSharedTable(t *testing.T) {
	b, err := os.ReadFile("testdata/cidr_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if canon, err := os.ReadFile("../../../testdata/cidr_cases.json"); err == nil && !bytes.Equal(canon, b) {
		t.Error("testdata/cidr_cases.json differs from the canonical <repo>/testdata copy")
	}
	var tbl struct {
		Normalize []struct {
			In     string `json:"in"`
			Public bool   `json:"public"`
			Want   string `json:"want"`
		} `json:"normalize"`
	}
	if err := json.Unmarshal(b, &tbl); err != nil {
		t.Fatal(err)
	}
	for _, c := range tbl.Normalize {
		got, err := normalizeCIDR(c.In)
		if err == nil && isPublicCIDR(got) && !c.Public {
			err = os.ErrPermission // the CLI gates /0 with --allow-public
			got = ""
		}
		if (err != nil) != (c.Want == "") || got != c.Want {
			t.Errorf("normalizeCIDR(%q, public=%v)=%q,%v want %q", c.In, c.Public, got, err, c.Want)
		}
	}
}

func TestIPFamilyLabels(t *testing.T) {
	if ipFamilyLabel("2001:db8::1") != "IPv6" || otherFamilyLabel("2001:db8::1") != "IPv4" ||
		ipFamilyLabel("1.2.3.4") != "IPv4" || otherFamilyLabel("1.2.3.4") != "IPv6" || ipFamilyLabel("::ffff:1.2.3.4") != "IPv4" {
		t.Fatal("labels")
	}
}
