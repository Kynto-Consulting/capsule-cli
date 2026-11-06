package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gw "github.com/kynto-consulting/capsule/cli/pkg/gatewayclient"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

func TestSFTPClientArgsAndGuide(t *testing.T) {
	raw, u, err := parseTarget("capsule://shop/sftp", "")
	if err != nil {
		t.Fatal(err)
	}
	proxy := proxyCommand("/usr/bin/capsule", raw, "", "linux")
	args := sftpClientArgs(u, proxy, "shop-acme-files", []string{"-b", "cmds.txt"})
	want := []string{"-o", "ProxyCommand=" + proxy, "-o", "HostKeyAlias=shop-sftp.capsule", "-b", "cmds.txt", "shop-acme-files@shop-sftp.capsule"}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("args: %v", args)
	}
	g := sftpGuide("files", &sftpInfo{User: "shop-acme-files", GatewayURI: raw, InternalAddr: "host.docker.internal:2222", Fingerprint: "SHA256:abc"},
		u, "C:/Program Files/capsule/capsule.exe", raw, "", "windows")
	for _, must := range []string{"capsule storage sftp files", "sftp -o ProxyCommand=", "scp  -o ProxyCommand=", "WinSCP", "Local", "FileZilla",
		"capsule tunnel shop/sftp --listen 127.0.0.1:2222", "SHA256:abc", "host.docker.internal:2222", "ssh-keys add files", "Host shop-sftp.capsule"} {
		if !strings.Contains(g, must) {
			t.Errorf("guide lacks %q:\n%s", must, g)
		}
	}
}

func TestSSHKeysCommands(t *testing.T) {
	var gotBody map[string]string
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == apiRoot && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "b1", "name": "files", "bucket": "shop-acme-files", "engine": "objstore"}}})
		case r.URL.Path == apiRoot+"/b1/ssh-keys" && r.Method == "POST":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "k1", "name": "laptop", "fingerprint": "SHA256:xyz"})
		case r.URL.Path == apiRoot+"/b1/ssh-keys" && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "k1", "name": "laptop", "fingerprint": "SHA256:xyz", "created_at": "2026-01-01T00:00:00Z"}}})
		case strings.HasPrefix(r.URL.Path, apiRoot+"/b1/ssh-keys/") && r.Method == "DELETE":
			deleted = strings.TrimPrefix(r.URL.Path, apiRoot+"/b1/ssh-keys/")
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	apiClient = client.New(srv.URL, "t")

	keyFile := filepath.Join(t.TempDir(), "id.pub")
	os.WriteFile(keyFile, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl laptop\n"), 0o600)
	t.Setenv("CAPSULE_ORG_ID", "o1")
	t.Setenv("CAPSULE_PROJECT_ID", "p1")

	storageSSHKeysAddCmd.Flags().Set("file", keyFile)
	storageSSHKeysAddCmd.Flags().Set("name", "laptop")
	if err := storageSSHKeysAddCmd.RunE(storageSSHKeysAddCmd, []string{"files"}); err != nil {
		t.Fatal(err)
	}
	if gotBody["name"] != "laptop" || !strings.HasPrefix(gotBody["public_key"], "ssh-ed25519 ") || strings.Contains(gotBody["public_key"], "\n") {
		t.Fatalf("body: %v", gotBody)
	}
	if err := storageSSHKeysListCmd.RunE(storageSSHKeysListCmd, []string{"files"}); err != nil {
		t.Fatal(err)
	}
	if err := storageSSHKeysRmCmd.RunE(storageSSHKeysRmCmd, []string{"files", "laptop"}); err != nil || deleted != "laptop" {
		t.Fatalf("rm: %v %q", err, deleted)
	}
	var _ = gw.DefaultZone
}

func TestReadPublicKeySources(t *testing.T) {
	if k, err := readPublicKey("", "ssh-ed25519 AAAA x"); err != nil || k != "ssh-ed25519 AAAA x" {
		t.Fatal(k, err)
	}
	if _, err := readPublicKey(filepath.Join(t.TempDir(), "missing.pub"), ""); err == nil {
		t.Fatal("missing file accepted")
	}
}
