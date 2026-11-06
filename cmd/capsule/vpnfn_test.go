package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

const (
	testOrg  = "11111111-1111-1111-1111-111111111111"
	testProj = "22222222-2222-2222-2222-222222222222"
)

type seenReq struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

// apiStub serves canned JSON per "METHOD path" and records every request. The CLI under test talks to it through
// apiClient; nothing here touches the real config or a real backend.
type apiStub struct {
	t    *testing.T
	mu   sync.Mutex
	seen []seenReq
	// reply maps "METHOD /path" to (status, body).
	reply map[string]struct {
		code int
		body string
	}
}

func newStub(t *testing.T) *apiStub {
	t.Helper()
	s := &apiStub{t: t, reply: map[string]struct {
		code int
		body string
	}{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		s.mu.Lock()
		s.seen = append(s.seen, seenReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), body})
		rep, ok := s.reply[r.Method+" "+r.URL.Path]
		s.mu.Unlock()
		if !ok {
			rep = struct {
				code int
				body string
			}{404, `{"error":{"code":"NOT_FOUND","message":"no stub"}}`}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.code)
		io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	apiClient = client.New(srv.URL, "tok-test")
	t.Setenv("CAPSULE_ORG_ID", testOrg)
	t.Setenv("CAPSULE_PROJECT_ID", testProj)
	return s
}

func (s *apiStub) on(method, path string, code int, body string) {
	s.mu.Lock()
	s.reply[method+" "+path] = struct {
		code int
		body string
	}{code, body}
	s.mu.Unlock()
}

func (s *apiStub) last() seenReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) == 0 {
		s.t.Fatal("no request reached the API")
	}
	return s.seen[len(s.seen)-1]
}

func (s *apiStub) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.seen) }

func vpnPath(rest string) string {
	return "/api/v1/orgs/" + testOrg + "/projects/" + testProj + "/vpn" + rest
}
func fnPath(rest string) string {
	return "/api/v1/orgs/" + testOrg + "/projects/" + testProj + "/fn" + rest
}

// runCmd executes cmd.RunE with the given flags and arguments and returns what it printed. Flags are reset first, so the
// shared cobra commands behave the same in every test.
func runCmd(t *testing.T, cmd *cobra.Command, stdin string, args []string, flags map[string]string) (string, error) {
	t.Helper()
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	})
	for k, v := range flags {
		for _, one := range strings.Split(v, "\x00") { // repeatable flags
			if err := cmd.Flags().Set(k, one); err != nil {
				t.Fatalf("flag --%s: %v", k, err)
			}
		}
	}
	oldOut, oldIn := os.Stdout, os.Stdin
	rOut, wOut, _ := os.Pipe()
	os.Stdout = wOut
	defer func() { os.Stdout, os.Stdin = oldOut, oldIn }()
	if stdin != "" {
		rIn, wIn, _ := os.Pipe()
		go func() { io.WriteString(wIn, stdin); wIn.Close() }()
		os.Stdin = rIn
	}
	outCh := make(chan string)
	go func() { b, _ := io.ReadAll(rOut); outCh <- string(b) }()
	err := cmd.RunE(cmd, args)
	wOut.Close()
	return <-outCh, err
}

const wgSecret = `{"private_key":"PRIV-KEY-VALUE","peer_public_key":"PEER","address":"10.99.0.2/32"}`

func TestVPNCreateReadsCredentialsFromStdinOnlyAndNeverPrintsThem(t *testing.T) {
	s := newStub(t)
	s.on("POST", vpnPath(""), 201, `{"tunnel":{"name":"estudio","provider":"wireguard","host":"1.2.3.4:51820","cidrs":["10.48.0.0/24"],"enabled":true},"warning":"cert pin recommended"}`)

	// without --secret-stdin nothing is sent and the credentials are not accepted another way
	if _, err := runCmd(t, vpnCreateCmd, "", []string{"estudio"}, map[string]string{"provider": "wireguard", "host": "1.2.3.4:51820", "cidr": "10.48.0.0/24"}); err == nil || !strings.Contains(err.Error(), "--secret-stdin") {
		t.Fatalf("expected --secret-stdin error, got %v", err)
	}
	if s.count() != 0 {
		t.Fatal("a request was sent without credentials")
	}

	out, err := runCmd(t, vpnCreateCmd, wgSecret+"\n", []string{"estudio"}, map[string]string{
		"provider": "wireguard", "host": "1.2.3.4:51820", "cidr": "10.48.0.0/24\x0010.50.0.0/16", "secret-stdin": "true", "max-conns": "6", "bandwidth": "1048576"})
	if err != nil {
		t.Fatal(err)
	}
	req := s.last()
	if req.Method != "POST" || req.Auth != "Bearer tok-test" {
		t.Fatalf("%+v", req)
	}
	if req.Body["secret"] != wgSecret || req.Body["provider"] != "wireguard" || req.Body["name"] != "estudio" || req.Body["max_conns"] != float64(6) || req.Body["bandwidth_bps"] != float64(1048576) {
		t.Fatalf("body: %+v", req.Body)
	}
	if cidrs, _ := req.Body["cidrs"].([]any); len(cidrs) != 2 {
		t.Fatalf("cidrs: %+v", req.Body["cidrs"])
	}
	if strings.Contains(out, "PRIV-KEY-VALUE") || !strings.Contains(out, "encrypted project secret") || !strings.Contains(out, "cert pin recommended") {
		t.Fatalf("output: %q", out)
	}
}

func TestVPNStatusTableAndJSON(t *testing.T) {
	s := newStub(t)
	s.on("GET", vpnPath(""), 200, `[{"name":"estudio","provider":"wireguard","host":"h","cidrs":["10.48.0.0/24"],"enabled":true,"status":"up","last_error":"","last_handshake_at":"2026-11-05T01:00:00Z"}]`)
	out, err := runCmd(t, vpnStatusCmd, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "estudio", "wireguard", "up", "10.48.0.0/24", "2026-11-05T01:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	s.on("GET", vpnPath("/estudio"), 200, `{"name":"estudio","provider":"wireguard","status":"error","last_error":"no handshake","enabled":true}`)
	out, _ = runCmd(t, vpnStatusCmd, "", []string{"estudio"}, nil)
	if !strings.Contains(out, "no handshake") {
		t.Fatalf("%q", out)
	}
	s.on("GET", vpnPath(""), 200, `[]`)
	if out, _ = runCmd(t, vpnStatusCmd, "", nil, nil); !strings.Contains(out, "No tunnel in this project") {
		t.Fatalf("%q", out)
	}
}

func TestVPNKillSwitchOnDeleteAndLogsOrder(t *testing.T) {
	s := newStub(t)
	s.on("PUT", vpnPath("/estudio/enabled"), 200, `{"name":"estudio","enabled":false}`)
	out, err := runCmd(t, vpnOffCmd, "", []string{"estudio"}, nil)
	if err != nil || !strings.Contains(out, "OFF") {
		t.Fatalf("%q %v", out, err)
	}
	if r := s.last(); r.Method != "PUT" || r.Body["enabled"] != false {
		t.Fatalf("%+v", r)
	}
	s.on("PUT", vpnPath("/estudio/enabled"), 200, `{"name":"estudio","enabled":true}`)
	if out, _ = runCmd(t, vpnOnCmd, "", []string{"estudio"}, nil); !strings.Contains(out, "enabled") || s.last().Body["enabled"] != true {
		t.Fatalf("%q", out)
	}
	s.on("DELETE", vpnPath("/estudio"), 204, ``)
	if out, err = runCmd(t, vpnDeleteCmd, "", []string{"estudio"}, nil); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("%q %v", out, err)
	}
	// the API returns newest first; the CLI prints oldest first, like a log
	s.on("GET", vpnPath("/estudio/events"), 200, `[{"at":"T2","kind":"disabled","actor":"user:ana","message":"kill switch"},{"at":"T1","kind":"created","actor":"user:ana","message":"created"}]`)
	out, _ = runCmd(t, vpnLogsCmd, "", []string{"estudio"}, map[string]string{"limit": "20"})
	if strings.Index(out, "created") > strings.Index(out, "kill switch") || s.last().Query != "limit=20" {
		t.Fatalf("%q %q", out, s.last().Query)
	}
}

func TestVPNErrorsFromTheAPIAreReturned(t *testing.T) {
	s := newStub(t)
	s.on("DELETE", vpnPath("/nada"), 404, `{"error":{"code":"NOT_FOUND","message":"tunnel not found"}}`)
	if _, err := runCmd(t, vpnDeleteCmd, "", []string{"nada"}, nil); err == nil || !strings.Contains(err.Error(), "tunnel not found") {
		t.Fatalf("%v", err)
	}
}

func TestFnDeploySendsRuntimeScopesAndLimits(t *testing.T) {
	s := newStub(t)
	s.on("POST", fnPath("/functions"), 201, `{"name":"migrar","image":"img","runtime":"lambda","memory_mb":1024,"timeout_seconds":7200,"max_concurrency":2,"scopes":["api:importacion","storage:biblioteca"]}`)
	out, err := runCmd(t, fnDeployCmd, "", []string{"migrar"}, map[string]string{
		"image": "123.dkr.ecr.us-east-1.amazonaws.com/migrar:1", "runtime": "lambda", "vpn": "estudio", "cron": "0 2 * * *", "memory": "1g", "timeout": "2h",
		"concurrency": "2", "scope": "api:importacion\x00storage:biblioteca"})
	if err != nil {
		t.Fatal(err)
	}
	b := s.last().Body
	if b["runtime"] != "lambda" || b["vpn"] != "estudio" || b["cron"] != "0 2 * * *" || b["memory"] != "1g" || b["timeout"] != "2h" || b["max_concurrency"] != float64(2) {
		t.Fatalf("%+v", b)
	}
	if sc, _ := b["scopes"].([]any); len(sc) != 2 || sc[1] != "storage:biblioteca" {
		t.Fatalf("scopes %+v", b["scopes"])
	}
	if !strings.Contains(out, "runtime lambda") || !strings.Contains(out, "api:importacion,storage:biblioteca") {
		t.Fatalf("%q", out)
	}
	// the default stays the container on the work node
	if _, err := runCmd(t, fnDeployCmd, "", []string{"migrar"}, map[string]string{"image": "img"}); err != nil || s.last().Body["runtime"] != "container" {
		t.Fatalf("%v %+v", err, s.last().Body)
	}
	// the API refuses an unknown runtime: the message reaches the user
	s.on("POST", fnPath("/functions"), 422, `{"error":{"code":"VALIDATION_ERROR","message":"invalid function: runtime must be \"container\" or \"lambda\""}}`)
	if _, err := runCmd(t, fnDeployCmd, "", []string{"x"}, map[string]string{"image": "img", "runtime": "wasm"}); err == nil || !strings.Contains(err.Error(), "runtime must be") {
		t.Fatalf("%v", err)
	}
}

func TestFnListRunsLogsCancelDeleteAndTriggers(t *testing.T) {
	s := newStub(t)
	s.on("GET", fnPath("/functions"), 200, `[{"name":"migrar","image":"img","runtime":"container","cron":"0 2 * * *","memory_mb":512,"timeout_seconds":3600,"scopes":["api:importacion"]}]`)
	out, err := runCmd(t, fnListCmd, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RUNTIME", "migrar", "container", "0 2 * * *", "512m", "3600s", "api:importacion"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}

	s.on("POST", fnPath("/functions/migrar/run"), 202, `{"id":"aaaaaaaa-0000-0000-0000-000000000000","trigger":"api","status":"queued"}`)
	out, err = runCmd(t, fnRunCmd, "", []string{"migrar"}, map[string]string{"payload": `{"lote":7}`})
	if err != nil || !strings.Contains(out, "queued") {
		t.Fatalf("%q %v", out, err)
	}
	if p, _ := s.last().Body["payload"].(map[string]any); p["lote"] != float64(7) {
		t.Fatalf("payload %+v", s.last().Body)
	}
	s.on("POST", fnPath("/functions/migrar/enqueue"), 202, `{"id":"bbbbbbbb-0000-0000-0000-000000000000","trigger":"queue","status":"queued"}`)
	if out, _ = runCmd(t, fnEnqueueCmd, "", []string{"migrar"}, nil); !strings.Contains(out, "(queue)") {
		t.Fatalf("%q", out)
	}

	s.on("GET", fnPath("/functions/migrar/runs"), 200, `[{"id":"aaaaaaaa-0000-0000-0000-000000000000","status":"succeeded","trigger":"api","triggered_by":"user:ana","queued_at":"T","bytes_in":640000,"bytes_out":10}]`)
	if out, _ = runCmd(t, fnRunsCmd, "", []string{"migrar"}, nil); !strings.Contains(out, "aaaaaaaa") || !strings.Contains(out, "640000") || strings.Contains(out, "aaaaaaaa-0000") {
		t.Fatalf("%q", out)
	}
	s.on("GET", fnPath("/runs/aaaaaaaa"), 200, `{"id":"aaaaaaaa-0000-0000-0000-000000000000","status":"failed","bytes_in":1,"bytes_out":2,"log_tail":"boom ***\n"}`)
	if out, _ = runCmd(t, fnLogsCmd, "", []string{"aaaaaaaa"}, nil); !strings.Contains(out, "failed") || !strings.Contains(out, "boom ***") {
		t.Fatalf("%q", out)
	}
	s.on("POST", fnPath("/runs/aaaaaaaa/cancel"), 200, `{"id":"aaaaaaaa-0000-0000-0000-000000000000","status":"cancelled"}`)
	if out, _ = runCmd(t, fnCancelCmd, "", []string{"aaaaaaaa"}, nil); !strings.Contains(out, "cancelled") {
		t.Fatalf("%q", out)
	}
	s.on("DELETE", fnPath("/functions/migrar"), 204, ``)
	if out, err = runCmd(t, fnDeleteCmd, "", []string{"migrar"}, nil); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("%q %v", out, err)
	}
}
