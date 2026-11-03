package gitcred

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseRequest(t *testing.T) {
	in := "protocol=https\nhost=git.example.com\npath=org/repo.git\nusername=u\nwwwauth[]=Basic\n\nignored=1\n"
	r, err := ParseRequest(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if r.Protocol != "https" || r.Host != "git.example.com" || r.Path != "org/repo.git" || r.Username != "u" || r.Other["wwwauth[]"] != "Basic" {
		t.Fatalf("%+v", r)
	}
	if _, ok := r.Other["ignored"]; ok {
		t.Fatal("must stop at the blank line")
	}
	if _, err := ParseRequest(strings.NewReader("SECRETVALUEnoequalsign")); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("malformed line must error without echoing it: %v", err)
	}
}

func TestNormalizePath(t *testing.T) {
	for in, want := range map[string]string{"org/repo.git": "org/repo", "/org/repo/": "org/repo", "org/repo": "org/repo"} {
		if got := NormalizePath(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestCredentialWrite(t *testing.T) {
	var b bytes.Buffer
	exp := time.Unix(1893456000, 0)
	if err := (Credential{Username: "ana", Password: "cgt_x", ExpiresAt: exp}).Write(&b); err != nil {
		t.Fatal(err)
	}
	if b.String() != "username=ana\npassword=cgt_x\npassword_expiry_utc=1893456000\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestCacheRenewsUnderTenMinutes(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	c := NewCache()
	c.Now = func() time.Time { return now }
	n := 0
	issue := func() (Credential, error) {
		n++
		return Credential{Username: "u", Password: "tok" + string(rune('0'+n)), ExpiresAt: now.Add(time.Hour)}, nil
	}
	k := Key("Git.Example.com", "org/repo.git")
	a, reused, _ := c.Get(k, issue)
	if reused || a.Password != "tok1" {
		t.Fatal("first call must issue")
	}
	now = now.Add(49 * time.Minute) // 11 min left: reuse
	b, reused, _ := c.Get(Key("git.example.com", "/org/repo"), issue)
	if !reused || b.Password != "tok1" || n != 1 {
		t.Fatalf("reuse expected, got reused=%v n=%d", reused, n)
	}
	now = now.Add(2 * time.Minute) // 9 min left: renew
	d, reused, _ := c.Get(k, issue)
	if reused || d.Password != "tok2" || n != 2 {
		t.Fatalf("renewal expected, got reused=%v n=%d", reused, n)
	}
	// server hint renew_after wins
	c.Forget(k)
	hint := func() (Credential, error) {
		return Credential{Password: "h", ExpiresAt: now.Add(time.Hour), RenewAt: now.Add(5 * time.Minute)}, nil
	}
	_, _, _ = c.Get(k, hint)
	now = now.Add(6 * time.Minute)
	if _, reused, _ = c.Get(k, issue); reused {
		t.Fatal("renew_after must force renewal")
	}
	// errors are not cached
	c.Forget(k)
	if _, _, err := c.Get(k, func() (Credential, error) { return Credential{}, errors.New("boom") }); err == nil {
		t.Fatal("error expected")
	}
}
