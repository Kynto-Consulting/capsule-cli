package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

func TestNormalizeCIDR(t *testing.T) {
	ok := map[string]string{
		"1.2.3.4":        "1.2.3.4/32",
		" 1.2.3.4 ":      "1.2.3.4/32",
		"10.0.0.5/24":    "10.0.0.0/24",
		"203.0.113.7/32": "203.0.113.7/32",
		"2001:db8::1":    "2001:db8::1/128",
	}
	for in, want := range ok {
		got, err := normalizeCIDR(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "1.2.3.4/33", "300.1.1.1"} {
		if _, err := normalizeCIDR(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestNormalizeCIDRsPublic(t *testing.T) {
	if _, err := normalizeCIDRs([]string{"0.0.0.0/0"}, false); err == nil {
		t.Fatal("0.0.0.0/0 must be rejected without allow-public")
	}
	if _, err := normalizeCIDRs([]string{"1.2.3.4", "::/0"}, false); err == nil {
		t.Fatal("::/0 must be rejected")
	}
	got, err := normalizeCIDRs([]string{"0.0.0.0/0"}, true)
	if err != nil || len(got) != 1 || !isPublicCIDR(got[0]) {
		t.Fatalf("%v %v", got, err)
	}
	got, _ = normalizeCIDRs([]string{"1.1.1.1", "1.1.1.1/32"}, false)
	if len(got) != 1 {
		t.Fatalf("dedupe: %v", got)
	}
}

func TestMergeSubtract(t *testing.T) {
	m := mergeCIDRs([]string{"b/32", "a/32"}, []string{"a/32", "c/32"})
	if !reflect.DeepEqual(m, []string{"a/32", "b/32", "c/32"}) {
		t.Fatal(m)
	}
	k, miss := subtractCIDRs([]string{"a", "b"}, []string{"b", "z"})
	if !reflect.DeepEqual(k, []string{"a"}) || !reflect.DeepEqual(miss, []string{"z"}) {
		t.Fatal(k, miss)
	}
}

func TestFriendlyErr(t *testing.T) {
	e := friendlyNetErr(errString("API error 403: FORBIDDEN — x"))
	if !strings.Contains(e.Error(), "deploy") {
		t.Fatal(e)
	}
	if !strings.Contains(friendlyNetErr(errString("API error 501: X — y")).Error(), "APP_SECURITY_GROUP_ID") {
		t.Fatal("501")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestPickRuleAndDB(t *testing.T) {
	rules := []accessRule{{ID: "aaaa1111-x", CIDR: "1.1.1.1/32"}, {ID: "aaaa2222-x", CIDR: "2.2.2.2/32"}, {ID: "bbbb3333-x", CIDR: "3.3.3.0/24"}}
	if r, err := pickRule(rules, "bbbb"); err != nil || r.CIDR != "3.3.3.0/24" {
		t.Fatal(r, err)
	}
	if _, err := pickRule(rules, "aaaa"); err == nil {
		t.Fatal("ambiguous expected")
	}
	if r, err := pickRule(rules, "2.2.2.2"); err != nil || r.ID != "aaaa2222-x" {
		t.Fatal(r, err)
	}
	dbs := []dbItem{{ID: "11111111-a", Name: "main"}, {ID: "22222222-b", Name: "cache"}}
	if d, err := pickDB(dbs, "main"); err != nil || d.ID != "11111111-a" {
		t.Fatal(d, err)
	}
	if d, err := pickDB(dbs, "2222"); err != nil || d.Name != "cache" {
		t.Fatal(d, err)
	}
	if _, err := pickDB(dbs, "zzz"); err == nil {
		t.Fatal("not found expected")
	}
}

// ── mock backend ────────────────────────────────────────────────────────────

type mock struct {
	mu        sync.Mutex
	rules     []accessRule
	allowlist []string
	bodies    []map[string]any
	status    int // forced status for access/network endpoints (0 = normal)
}

const (
	tOrg  = "00000000-0000-0000-0000-0000000000a1"
	tProj = "00000000-0000-0000-0000-0000000000b2"
	tDB   = "11111111-1111-1111-1111-111111111111"
)

func newMock(t *testing.T) (*mock, func()) {
	m := &mock{}
	base := "/api/v1/orgs/" + tOrg + "/projects/" + tProj
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
			m.bodies = append(m.bodies, body)
		}
		p := r.URL.Path
		if m.status != 0 && (strings.Contains(p, "/access") || strings.HasSuffix(p, "/network")) {
			w.WriteHeader(m.status)
			_, _ = w.Write([]byte(`{"error":{"code":"X","message":"nope"}}`))
			return
		}
		switch {
		case p == "/api/v1/network/my-ip":
			_, _ = w.Write([]byte(`{"ip":"198.51.100.9"}`))
		case p == base+"/databases":
			_, _ = w.Write([]byte(`{"data":[{"id":"` + tDB + `","name":"main","engine":"postgres","host":"db.example.com","port":5432,"db_name":"app","status":"running"}]}`))
		case p == base+"/databases/"+tDB+"/access" && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(accessResp{Rules: m.rules, Host: "db.example.com", Port: 5432, TLS: true})
		case p == base+"/databases/"+tDB+"/access" && r.Method == "POST":
			m.rules = append(m.rules, accessRule{ID: "r" + string(rune('a'+len(m.rules))) + "000000-0000", CIDR: body["cidr"].(string)})
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		case p == base+"/databases/"+tDB+"/access" && r.Method == "PUT":
			m.rules = nil
			for _, it := range body["cidrs"].([]any) {
				m.rules = append(m.rules, accessRule{ID: "s" + string(rune('a'+len(m.rules))) + "000000-0000", CIDR: it.(map[string]any)["cidr"].(string)})
			}
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(p, base+"/databases/"+tDB+"/access/") && r.Method == "DELETE":
			id := strings.TrimPrefix(p, base+"/databases/"+tDB+"/access/")
			var keep []accessRule
			for _, ru := range m.rules {
				if ru.ID != id {
					keep = append(keep, ru)
				}
			}
			m.rules = keep
			w.WriteHeader(204)
		case p == base+"/network" && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"ip_allowlist": m.allowlist})
		case p == base+"/network" && r.Method == "PUT":
			m.allowlist = nil
			for _, c := range body["ip_allowlist"].([]any) {
				m.allowlist = append(m.allowlist, c.(string))
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"NF","message":"` + r.Method + " " + p + `"}}`))
		}
	}))
	old := apiClient
	apiClient = client.New(srv.URL, "tok")
	t.Setenv("CAPSULE_ORG_ID", tOrg)
	t.Setenv("CAPSULE_PROJECT_ID", tProj)
	return m, func() { srv.Close(); apiClient = old }
}

// run executes a command's RunE with the given flags, resetting them after.
func run(t *testing.T, c *cobra.Command, args []string, flags map[string][]string) error {
	t.Helper()
	for k, vs := range flags {
		for _, v := range vs {
			if err := c.Flags().Set(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer c.Flags().VisitAll(func(f *pflag.Flag) {
		f.Changed = false
		if sv, ok := f.Value.(interface{ Replace([]string) error }); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
	})
	return c.RunE(c, args)
}

func TestDBAccessFlow(t *testing.T) {
	m, done := newMock(t)
	defer done()

	if err := run(t, dbAccessAddCmd, []string{"main"}, map[string][]string{"cidr": {"203.0.113.7"}}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, dbAccessAddCmd, []string{"1111"}, map[string][]string{"my-ip": {"true"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.rules) != 2 || m.rules[0].CIDR != "203.0.113.7/32" || m.rules[1].CIDR != "198.51.100.9/32" {
		t.Fatalf("%+v", m.rules)
	}
	// remove by short rule id
	if err := run(t, dbAccessRemoveCmd, []string{"main", "rb00"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.rules) != 1 {
		t.Fatalf("%+v", m.rules)
	}
	// remove by cidr
	if err := run(t, dbAccessRemoveCmd, []string{"main"}, map[string][]string{"cidr": {"203.0.113.7/32"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.rules) != 0 {
		t.Fatalf("%+v", m.rules)
	}
	// set
	if err := run(t, dbAccessSetCmd, []string{"main"}, map[string][]string{"cidr": {"10.0.0.0/8", "1.2.3.4"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.rules) != 2 {
		t.Fatalf("%+v", m.rules)
	}
	last := m.bodies[len(m.bodies)-1]
	if _, has := last["allow_public"]; has {
		t.Fatal("allow_public must not be sent unless explicit")
	}
	// public without --allow-public is rejected locally, nothing sent
	n := len(m.bodies)
	if err := run(t, dbAccessAddCmd, []string{"main"}, map[string][]string{"cidr": {"0.0.0.0/0"}}); err == nil {
		t.Fatal("expected rejection")
	}
	if len(m.bodies) != n {
		t.Fatal("request must not be sent")
	}
	// public with --allow-public --yes sends allow_public
	err := run(t, dbAccessAddCmd, []string{"main"}, map[string][]string{"cidr": {"0.0.0.0/0"}, "allow-public": {"true"}, "yes": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	var posted map[string]any
	for _, b := range m.bodies {
		if b["cidr"] == "0.0.0.0/0" {
			posted = b
		}
	}
	if posted == nil || posted["allow_public"] != true {
		t.Fatalf("%v", posted)
	}
}

func TestDBListAndErrors(t *testing.T) {
	m, done := newMock(t)
	defer done()
	if err := run(t, dbListCmd, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := run(t, dbAccessListCmd, []string{"nope"}, nil); err == nil {
		t.Fatal("unknown db must fail")
	}
	m.status = 403
	err := run(t, dbAccessListCmd, []string{"main"}, nil)
	if err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("403 message: %v", err)
	}
	m.status = 501
	err = run(t, dbAccessListCmd, []string{"main"}, nil)
	if err == nil || !strings.Contains(err.Error(), "APP_SECURITY_GROUP_ID") {
		t.Fatalf("501 message: %v", err)
	}
}

func TestNetworkFlow(t *testing.T) {
	m, done := newMock(t)
	defer done()
	if err := run(t, networkAllowCmd, nil, map[string][]string{"cidr": {"203.0.113.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, networkAllowCmd, nil, map[string][]string{"my-ip": {"true"}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.allowlist, []string{"198.51.100.9/32", "203.0.113.0/24"}) {
		t.Fatal(m.allowlist)
	}
	if err := run(t, networkDenyCmd, nil, map[string][]string{"cidr": {"203.0.113.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, networkSetCmd, nil, map[string][]string{"cidr": {"9.9.9.9", "8.8.8.8/32"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.allowlist) != 2 {
		t.Fatal(m.allowlist)
	}
	if err := run(t, networkAllowCmd, nil, map[string][]string{"cidr": {"0.0.0.0/0"}}); err == nil {
		t.Fatal("0.0.0.0/0 must be rejected on network")
	}
	if err := run(t, networkClearCmd, nil, map[string][]string{"yes": {"true"}}); err != nil {
		t.Fatal(err)
	}
	if len(m.allowlist) != 0 {
		t.Fatal(m.allowlist)
	}
	if err := run(t, networkShowCmd, nil, nil); err != nil {
		t.Fatal(err)
	}
}
