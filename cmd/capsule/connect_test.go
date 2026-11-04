package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kynto-consulting/capsule/cli/internal/client"
	"github.com/kynto-consulting/capsule/cli/internal/config"
	gw "github.com/kynto-consulting/capsule/cli/pkg/gatewayclient"
	"github.com/kynto-consulting/capsule/cli/pkg/gatewayclient/gwtest"
)

// gwtEnv prepara el entorno del CLI contra el servidor de referencia.
func gwtEnv(t *testing.T, s *gwtest.Server) {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, s.CAPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envGatewayCA, ca)
	t.Setenv(envGatewayToken, gwtest.DefaultToken)
	t.Setenv("CAPSULE_TOKEN", "")
	cfg, apiClient = nil, nil
}

func gwtParams(s *gwtest.Server, svc string, extra ...string) tunnelParams {
	return tunnelParams{target: s.URI(svc, extra...), stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard,
		retry: &gw.RetryPolicy{Initial: time.Millisecond, Max: 2 * time.Millisecond, Jitter: -1}}
}

func exitCodeOf(err error) int { return exitCodeFor(err) }

// El proceso hijo de --exec: comprueba el entorno inyectado, habla con el servicio
// por el puerto local y sale con el código 7.
func TestHelperProcessConnectExec(t *testing.T) {
	if os.Getenv("GWT_HELPER") != "1" {
		return
	}
	host, port := os.Getenv("PGHOST"), os.Getenv("PGPORT")
	fail := func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(90) }
	if host != "127.0.0.1" || port == "" || os.Getenv("CAPSULE_TUNNEL_ADDR") != net.JoinHostPort(host, port) {
		fail("PGHOST/PGPORT: %q %q", host, port)
	}
	if os.Getenv(envGatewayToken) != "" {
		fail("el token del gateway no debe llegar al hijo")
	}
	if want := "postgres://app:secreto@" + net.JoinHostPort(host, port) + "/midb?sslmode=disable"; os.Getenv("DATABASE_URL") != want {
		fail("DATABASE_URL: %q", os.Getenv("DATABASE_URL"))
	}
	c, err := net.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		fail("dial: %v", err)
	}
	c.Write([]byte("ping-exec"))
	buf := make([]byte, 9)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping-exec" {
		fail("eco: %q %v", buf, err)
	}
	os.Exit(7)
}

func TestConnectExecInjectsEnvAndPropagatesExit(t *testing.T) {
	s := gwtest.New(t, gwtest.Config{})
	gwtEnv(t, s)
	t.Setenv("GWT_HELPER", "1")
	t.Setenv("DATABASE_URL", "postgres://app:secreto@db.interna:5432/midb?sslmode=disable")
	t.Setenv("PGHOST", "otro")
	p := gwtParams(s, "echo")
	var errOut bytes.Buffer
	p.stderr = &errOut
	p.exec = []string{os.Args[0], "-test.run=^TestHelperProcessConnectExec$"}
	err := runTunnel(context.Background(), p)
	if exitCodeOf(err) != 7 {
		t.Fatalf("el código del hijo debe propagarse: %v (stderr %q)", err, errOut.String())
	}
	if strings.Contains(errOut.String(), gwtest.DefaultToken) || strings.Contains(errOut.String(), "secreto") {
		t.Fatalf("secretos en stderr: %q", errOut.String())
	}
}

func TestTunnelListenForwardsAndStops(t *testing.T) {
	s := gwtest.New(t, gwtest.Config{})
	gwtEnv(t, s)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	p := gwtParams(s, "echo")
	p.listen = addr
	var errOut bytes.Buffer
	p.stderr = &errOut
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runTunnel(ctx, p) }()
	var c net.Conn
	var err error
	for i := 0; i < 50; i++ {
		if c, err = net.Dial("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("hola túnel"))
	c.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(c)
	if string(got) != "hola túnel" {
		t.Fatalf("eco: %q", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Ctrl-C no es un error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("el túnel no se detuvo")
	}
	if !strings.Contains(errOut.String(), "Túnel local: "+addr) {
		t.Fatalf("mensaje: %q", errOut.String())
	}
}

func TestConnectStdioProxyCommand(t *testing.T) {
	s := gwtest.New(t, gwtest.Config{})
	gwtEnv(t, s)
	p := gwtParams(s, "banner")
	var out bytes.Buffer
	pr, pw := io.Pipe()
	p.stdin, p.stdout = pr, &out
	done := make(chan error, 1)
	go func() { done <- runStdio(context.Background(), p) }()
	time.Sleep(300 * time.Millisecond)
	pw.Close() // fin de stdin
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out.String() != "SSH-2.0-capsule-test\r\n" {
		t.Fatalf("stdout debe contener solo los bytes del servicio: %q", out.String())
	}
}

func TestExitCodesAndTokenNeverPrinted(t *testing.T) {
	s := gwtest.New(t, gwtest.Config{})
	gwtEnv(t, s)
	cases := []struct {
		svc  string
		code int
	}{
		{"err-0201", exitDenied}, {"err-0301", exitDenied}, {"err-0501", exitBlocked},
		{"err-0401", exitGeneric}, {"err-0202", exitAuth}, {"err-0109", exitGeneric}, {"leak", exitGeneric},
	}
	for _, c := range cases {
		t.Run(c.svc, func(t *testing.T) {
			p := gwtParams(s, c.svc)
			err := runStdio(context.Background(), p)
			if err == nil || exitCodeOf(err) != c.code {
				t.Fatalf("código %d, se esperaba %d: %v", exitCodeOf(err), c.code, err)
			}
			if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), gwtest.DefaultToken) {
				t.Fatal("el token aparece en el error")
			}
			if !strings.HasPrefix(err.Error(), "capsule: ") {
				t.Fatalf("mensaje: %q", err.Error())
			}
		})
	}
	// uso incorrecto
	if err := runStdio(context.Background(), tunnelParams{target: "capsule://P/x", stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}); exitCodeOf(err) != exitUsage {
		t.Fatalf("URI inválido: %v", err)
	}
	if err := runStdio(context.Background(), tunnelParams{target: "http://x/y", stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}); exitCodeOf(err) != exitUsage {
		t.Fatalf("esquema: %v", err)
	}
	// sin credenciales
	t.Setenv(envGatewayToken, "")
	if err := runStdio(context.Background(), gwtParams(s, "echo")); exitCodeOf(err) != exitAuth {
		t.Fatalf("sin token: %v", err)
	}
	t.Setenv(envGatewayToken, "no-es-un-token-"+strings.Repeat("a", 30))
	err := runStdio(context.Background(), gwtParams(s, "echo"))
	if exitCodeOf(err) != exitAuth || strings.Contains(err.Error(), "no-es-un-token") {
		t.Fatalf("token mal formado: %v", err)
	}
	// plazo agotado
	t.Setenv(envGatewayToken, gwtest.DefaultToken)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := runStdio(ctx, gwtParams(s, "hang")); exitCodeOf(err) != exitTimeout {
		t.Fatalf("plazo: %v", err)
	}
}

func TestTokenResolutionOrder(t *testing.T) {
	u, _ := gw.ParseURI("capsule://mi-proyecto/db", "")
	tokOf := func() string {
		ts, err := gatewayTokenSource(u)
		if err != nil {
			return "ERR:" + err.Error()
		}
		tok, err := ts.Token(context.Background())
		if err != nil {
			return "ERR:" + err.Error()
		}
		return tok
	}
	cap1, cap2, cgs := "cap_"+strings.Repeat("1", 40), "cap_"+strings.Repeat("2", 40), "cgs_"+strings.Repeat("3", 40)
	t.Setenv(envGatewayToken, "")
	t.Setenv("CAPSULE_TOKEN", "")
	cfg, apiClient = nil, nil
	if !strings.HasPrefix(tokOf(), "ERR:") {
		t.Fatal("sin ninguna fuente debe fallar")
	}
	cfg = &config.Config{Token: cgs}
	if tokOf() != cgs {
		t.Fatal("token guardado con prefijo del gateway")
	}
	t.Setenv("CAPSULE_TOKEN", cap2)
	if tokOf() != cap2 {
		t.Fatal("CAPSULE_TOKEN")
	}
	t.Setenv(envGatewayToken, cap1)
	if tokOf() != cap1 {
		t.Fatal("CAPSULE_GATEWAY_TOKEN gana")
	}
	cfg = nil
}

func TestMintConnectionTokenFromSession(t *testing.T) {
	var gotPath, gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/"+orgUUID+"/projects" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": projUUID, "slug": "mi-proyecto"}}})
			return
		}
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]string{"token": "cgc_" + strings.Repeat("9", 60), "expires_at": "2030-01-01T00:00:00Z"})
	}))
	defer api.Close()
	t.Setenv(envGatewayToken, "")
	t.Setenv("CAPSULE_TOKEN", "")
	cfg = &config.Config{Token: "eyJhbGciOiJIUzI1NiJ9.e30.firma", OrgID: orgUUID}
	apiClient = client.New(api.URL, cfg.Token)
	defer func() { cfg, apiClient = nil, nil }()
	u, _ := gw.ParseURI("capsule://mi-proyecto/db", "")
	ts, err := gatewayTokenSource(u)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token(context.Background())
	if err != nil || !strings.HasPrefix(tok, "cgc_") {
		t.Fatalf("%v", err)
	}
	if gotPath != "/api/v1/orgs/"+orgUUID+"/projects/"+projUUID+"/gateway/services/db/connection-token" || gotAuth != "Bearer "+cfg.Token {
		t.Fatalf("petición: %s %q", gotPath, gotAuth)
	}
}

func TestHelpersTargetsAndEnv(t *testing.T) {
	if r, err := normalizeTarget("mi-proyecto/db"); err != nil || r != "capsule://mi-proyecto/db" {
		t.Fatal(r, err)
	}
	if r, _ := normalizeTarget("CAPSULE://a/b"); r != "CAPSULE://a/b" {
		t.Fatal(r)
	}
	if _, err := normalizeTarget("https://a/b"); exitCodeOf(err) != exitUsage {
		t.Fatal(err)
	}
	if nu, ok := rewriteDatabaseURL("postgres://u:p%40ss@h.interna:5432/d?sslmode=require", "127.0.0.1", "6000"); !ok ||
		nu != "postgres://u:p%40ss@127.0.0.1:6000/d?sslmode=require" {
		t.Fatal(nu, ok)
	}
	if _, ok := rewriteDatabaseURL("no es una url", "h", "1"); ok {
		t.Fatal("URL inválida no se reescribe")
	}
	env := execEnv([]string{"A=1", "PGHOST=x", "pgport=9", envGatewayToken + "=cap_zzz", "DATABASE_URL=postgres://u@h/d"}, "127.0.0.1", "55")
	joined := strings.Join(env, "\n")
	for _, want := range []string{"A=1", "PGHOST=127.0.0.1", "PGPORT=55", "DATABASE_URL=postgres://u@127.0.0.1:55/d", "CAPSULE_TUNNEL_ADDR=127.0.0.1:55"} {
		if !strings.Contains(joined, want) {
			t.Errorf("falta %s en %v", want, env)
		}
	}
	if strings.Contains(joined, "PGHOST=x") || strings.Contains(joined, "pgport=9") || strings.Contains(joined, "cap_zzz") {
		t.Errorf("quedaron valores viejos: %v", env)
	}
}

func TestSSHProxyCommand(t *testing.T) {
	u, _ := gw.ParseURI("capsule://mi-proyecto/ssh?x-a=1", "")
	raw := "capsule://mi-proyecto/ssh?zone=apps.example.test&x-a=100%25"
	posix := proxyCommand("/usr/local/bin/capsule app", raw, "apps.example.test", "linux")
	if posix != `'/usr/local/bin/capsule app' connect --stdio --zone 'apps.example.test' 'capsule://mi-proyecto/ssh?zone=apps.example.test&x-a=100%%25'` {
		t.Fatal(posix)
	}
	win := proxyCommand(`C:\Program Files\capsule\capsule.exe`, "capsule://mi-proyecto/ssh", "", "windows")
	if win != `"C:/Program Files/capsule/capsule.exe" connect --stdio "capsule://mi-proyecto/ssh"` {
		t.Fatal(win)
	}
	args := sshArgs(u, posix, "deploy", []string{"-i", "k", "uptime"})
	want := []string{"-o", "ProxyCommand=" + posix, "-o", "HostKeyAlias=mi-proyecto-ssh.capsule", "-l", "deploy", "-i", "k", "mi-proyecto-ssh.capsule", "uptime"}
	if fmt.Sprint(args) != fmt.Sprint(want) {
		t.Fatalf("%v", args)
	}
	cfgBlock := sshConfigBlock(u, posix, "deploy")
	if !strings.Contains(cfgBlock, "Host mi-proyecto-ssh.capsule") || !strings.Contains(cfgBlock, "User deploy") || !strings.Contains(cfgBlock, "ProxyCommand "+posix) {
		t.Fatal(cfgBlock)
	}
	for _, a := range append(args, cfgBlock) { // el token nunca está en la línea de comandos
		if strings.Contains(a, "cap_") {
			t.Fatal("token en la línea de comandos de ssh")
		}
	}
}

func TestRequireNativeAndGatewayExit(t *testing.T) {
	if err := requireNative("connect"); err != nil {
		t.Fatalf("en el binario nativo no debe fallar: %v", err)
	}
	if gatewayExit(context.Canceled) != nil {
		t.Fatal("cancelación = salida normal")
	}
	err := gatewayExit(errors.New("algo raro"))
	if exitCodeOf(err) != exitGeneric || !strings.Contains(err.Error(), "algo raro") {
		t.Fatal(err)
	}
}

const (
	orgUUID  = "11111111-1111-4111-8111-111111111111"
	projUUID = "22222222-2222-4222-8222-222222222222"
)

func TestSSHArgsOrder(t *testing.T) {
	u, _ := gw.ParseURI("capsule://mi-proyecto/ssh", "")
	a := sshArgs(u, "PROXY", "deploy", []string{"-i", "key", "-p", "22", "uptime", "-a"})
	want := []string{"-o", "ProxyCommand=PROXY", "-o", "HostKeyAlias=mi-proyecto-ssh.capsule", "-l", "deploy", "-i", "key", "-p", "22", "mi-proyecto-ssh.capsule", "uptime", "-a"}
	if fmt.Sprint(a) != fmt.Sprint(want) {
		t.Fatalf("%v", a)
	}
	if a := sshArgs(u, "P", "", []string{"uptime"}); a[len(a)-2] != "mi-proyecto-ssh.capsule" || a[len(a)-1] != "uptime" {
		t.Fatalf("%v", a)
	}
	if a := sshArgs(u, "P", "", nil); a[len(a)-1] != "mi-proyecto-ssh.capsule" {
		t.Fatalf("%v", a)
	}
}
