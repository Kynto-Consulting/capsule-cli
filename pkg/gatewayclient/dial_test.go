package gatewayclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	gw "github.com/kynto-consulting/capsule/cli/pkg/gatewayclient"
	"github.com/kynto-consulting/capsule/cli/pkg/gatewayclient/gwtest"
)

// opts arma Options contra el servidor de referencia: CA fijada, sin esperas reales.
func opts(s *gwtest.Server) (*gw.Options, *[]time.Duration) {
	var mu sync.Mutex
	var slept []time.Duration
	return &gw.Options{
		Token:   gwtest.DefaultToken,
		RootCAs: s.CertPool(),
		Retry: &gw.RetryPolicy{Initial: time.Millisecond, Max: 5 * time.Millisecond, Jitter: -1,
			Sleep: func(ctx context.Context, d time.Duration) error {
				mu.Lock()
				slept = append(slept, d)
				mu.Unlock()
				return ctx.Err()
			}},
	}, &slept
}

func newSrv(t *testing.T) *gwtest.Server { return gwtest.New(t, gwtest.Config{}) }

func TestDialEchoAndRequestContents(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	c, err := gw.Dial(context.Background(), s.URI("echo"), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("hola")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "hola" {
		t.Fatalf("eco: %q %v", b, err)
	}
	seen := s.Seen()
	if len(seen) != 1 {
		t.Fatalf("solicitudes: %d", len(seen))
	}
	r := seen[0]
	if r.ALPN != "capsule/1" || r.TLSVer != 0x0304 || r.SNI != "gw.example.test" {
		t.Errorf("TLS: alpn=%q ver=%x sni=%q", r.ALPN, r.TLSVer, r.SNI)
	}
	if r.Request.Auth != gwtest.DefaultToken || r.Request.AuthType != gw.AuthCap || r.Request.Transport != 1 ||
		r.Request.Project != "proyecto" || r.Request.Service != "echo" || len(r.Request.Nonce) != 16 ||
		r.Request.ClientID != "capsule-go/"+gw.Version || r.Request.Flags != 0 {
		t.Errorf("solicitud: %v", r.Request)
	}
	if conn := c.(*gw.Conn); conn.Response().Status != 0 || conn.URI().Route() != "proyecto/echo" {
		t.Error("Response/URI")
	}
}

func TestAuthTypesByPrefix(t *testing.T) {
	for prefix, want := range map[string]byte{"cap_": 1, "cgt_": 2, "cgc_": 3, "cgs_": 4} {
		tok := prefix + strings.Repeat("Z", 40)
		s := gwtest.New(t, gwtest.Config{Tokens: []string{tok}})
		o, _ := opts(s)
		o.Token = tok
		c, err := gw.Dial(context.Background(), s.URI("echo"), o)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if got := s.Seen()[0].Request.AuthType; got != want {
			t.Errorf("%s: auth_type %d, se esperaba %d", prefix, got, want)
		}
	}
	s := newSrv(t)
	o, _ := opts(s)
	o.Token = "xyz_" + strings.Repeat("Z", 40)
	if _, err := gw.Dial(context.Background(), s.URI("echo"), o); !errors.Is(err, gw.ErrBadToken) {
		t.Fatalf("prefijo desconocido: %v", err)
	}
	if len(s.Seen()) != 0 {
		t.Fatal("un token inválido no debe llegar a la red")
	}
	o.Token, o.TokenSource = "", nil
	t.Setenv("CAPSULE_GATEWAY_TOKEN", "")
	t.Setenv("CAPSULE_TOKEN", "")
	if _, err := gw.Dial(context.Background(), s.URI("echo"), o); !errors.Is(err, gw.ErrNoToken) {
		t.Fatalf("sin token: %v", err)
	}
	t.Setenv("CAPSULE_TOKEN", gwtest.DefaultToken)
	c, err := gw.Dial(context.Background(), s.URI("echo"), o)
	if err != nil {
		t.Fatalf("token del entorno: %v", err)
	}
	c.Close()
}

// Cada código de PROTOCOL.md sección 7: tipo, status/code y reintentabilidad.
func TestStatusTable(t *testing.T) {
	rows := []struct {
		code      uint16
		kind      error
		retryable bool
		attempts  int // intentos esperados con MaxAttempts 5
	}{
		{0x0101, gw.ErrUnsupportedVersion, false, 1},
		{0x0102, gw.ErrBadRequest, false, 1},
		{0x0103, gw.ErrBadRequest, false, 1},
		{0x0104, gw.ErrBadRequest, false, 1},
		{0x0105, gw.ErrBadRequest, false, 1},
		{0x0106, gw.ErrBadRequest, false, 1},
		{0x0107, gw.ErrBadRequest, false, 1},
		{0x0108, gw.ErrBadRequest, false, 1},
		{0x0109, gw.ErrUnsupportedTransport, false, 1},
		{0x010A, gw.ErrBadRequest, true, 2}, // una sola vez
		{0x010B, gw.ErrBadRequest, false, 1},
		{0x0201, gw.ErrNotAuthorized, false, 1},
		{0x0202, gw.ErrTokenExpired, false, 1}, // sin fuente renovable
		{0x0203, gw.ErrNotAuthorized, false, 1},
		{0x0301, gw.ErrIpDenied, false, 1},
		{0x0401, gw.ErrUnavailable, true, 5},
		{0x0402, gw.ErrUnavailable, true, 5},
		{0x0501, gw.ErrQuotaExceeded, true, 5},
		{0x0502, gw.ErrQuotaExceeded, true, 5},
		{0x0503, gw.ErrQuotaExceeded, true, 5},
		{0x0504, gw.ErrQuotaExceeded, true, 5},
		{0x0601, gw.ErrGatewayBusy, true, 5},
		{0x0602, gw.ErrGatewayBusy, true, 5},
		{0x0603, gw.ErrGatewayBusy, true, 3}, // 1-2 veces
		{0x0901, gw.ErrUnknownStatus, false, 1},
		{0x0299, gw.ErrNotAuthorized, false, 1}, // code desconocido de un status conocido
	}
	s := newSrv(t)
	for _, row := range rows {
		t.Run(fmt.Sprintf("%04x", row.code), func(t *testing.T) {
			before := len(s.Seen())
			o, _ := opts(s)
			_, err := gw.Dial(context.Background(), s.URI(fmt.Sprintf("err-%04x", row.code)), o)
			if !errors.Is(err, row.kind) {
				t.Fatalf("errors.Is(%v): %v", row.kind, err)
			}
			var ge *gw.GatewayError
			if !errors.As(err, &ge) || ge.Code != row.code || int(ge.Status) != int(row.code>>8) {
				t.Fatalf("GatewayError: %+v", ge)
			}
			if gw.IsRetryable(err) != row.retryable {
				t.Errorf("IsRetryable = %v", gw.IsRetryable(err))
			}
			if n := len(s.Seen()) - before; n != row.attempts {
				t.Errorf("intentos %d, se esperaba %d", n, row.attempts)
			}
			if strings.Contains(err.Error(), gwtest.DefaultToken) {
				t.Error("el error contiene el token")
			}
		})
	}
}

func TestUniformNotAuthorized(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	bad := *o
	bad.Token = "cap_" + strings.Repeat("X", 43)
	_, e1 := gw.Dial(context.Background(), s.URI("echo"), &bad)
	_, e2 := gw.Dial(context.Background(), strings.Replace(s.URI("echo"), "/proyecto/", "/ajeno/", 1), o)
	if !errors.Is(e1, gw.ErrNotAuthorized) || !errors.Is(e2, gw.ErrNotAuthorized) || e1.Error() != e2.Error() {
		t.Fatalf("respuestas no uniformes: %v | %v", e1, e2)
	}
}

func TestNonceFreshPerAttemptAndRetries(t *testing.T) {
	s := newSrv(t)
	o, slept := opts(s)
	c, err := gw.Dial(context.Background(), s.URI("flaky-2"), o)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	seen := s.Seen()
	if len(seen) != 3 {
		t.Fatalf("intentos %d", len(seen))
	}
	uniq := map[string]bool{}
	for _, x := range seen {
		uniq[string(x.Request.Nonce)] = true
	}
	if len(uniq) != 3 {
		t.Fatal("el nonce debe ser nuevo en cada intento")
	}
	if len(*slept) != 2 || (*slept)[0] != time.Millisecond || (*slept)[1] != 2*time.Millisecond {
		t.Fatalf("retroceso: %v", *slept)
	}
}

func TestBackoffDefaultsAndRetryAfter(t *testing.T) {
	s := gwtest.New(t, gwtest.Config{RetryAfter: 2})
	var delays []time.Duration
	o := &gw.Options{Token: gwtest.DefaultToken, RootCAs: s.CertPool(), Retry: &gw.RetryPolicy{
		Jitter: -1, Sleep: func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return nil }}}
	_, err := gw.Dial(context.Background(), s.URI("err-0501"), o)
	var ge *gw.GatewayError
	if !errors.As(err, &ge) || ge.RetryAfter != 2*time.Second {
		t.Fatalf("retry_after: %v", err)
	}
	if len(delays) != 4 {
		t.Fatalf("esperas: %v", delays)
	}
	for _, d := range delays { // 500ms, 1s, 2s, 4s: retry_after=2s manda como mínimo
		if d < 2*time.Second {
			t.Fatalf("no respeta retry_after: %v", delays)
		}
	}
	if delays[3] != 4*time.Second {
		t.Fatalf("factor 2: %v", delays)
	}
	// Un retry_after mayor que el máximo tolerado se devuelve sin esperar.
	delays = nil
	o.Retry.MaxRetryAfter = time.Second
	if _, err := gw.Dial(context.Background(), s.URI("err-0501"), o); !errors.Is(err, gw.ErrQuotaExceeded) || len(delays) != 0 {
		t.Fatalf("%v %v", err, delays)
	}
}

func TestDefaultBackoffJitterAndCap(t *testing.T) {
	s := newSrv(t)
	var delays []time.Duration
	o := &gw.Options{Token: gwtest.DefaultToken, RootCAs: s.CertPool(), Retry: &gw.RetryPolicy{
		MaxAttempts: 9, Sleep: func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return nil }}}
	gw.Dial(context.Background(), s.URI("err-0401"), o)
	if len(delays) != 8 {
		t.Fatalf("esperas: %v", delays)
	}
	base := []float64{0.5, 1, 2, 4, 8, 16, 30, 30}
	for i, d := range delays {
		lo, hi := time.Duration(base[i]*0.8*float64(time.Second)), time.Duration(base[i]*1.2*float64(time.Second))
		if d < lo || d > hi {
			t.Errorf("espera %d = %v fuera de [%v, %v]", i, d, lo, hi)
		}
	}
}

func TestTokenExpiredRenewOnce(t *testing.T) {
	s := gwtest.New(t, gwtest.Config{Tokens: []string{gwtest.DefaultToken, "cap_" + strings.Repeat("N", 43)}})
	o, _ := opts(s)
	rs := &renewing{}
	o.Token, o.TokenSource = "", rs
	_, err := gw.Dial(context.Background(), s.URI("err-0202"), o)
	if !errors.Is(err, gw.ErrTokenExpired) {
		t.Fatal(err)
	}
	if rs.renews != 1 || len(s.Seen()) != 2 {
		t.Fatalf("renovaciones %d, intentos %d", rs.renews, len(s.Seen()))
	}
	if s.Seen()[1].Request.Auth != "cap_"+strings.Repeat("N", 43) {
		t.Fatal("el segundo intento debe usar el token renovado")
	}
}

type renewing struct{ renews int }

func (r *renewing) Token(context.Context) (string, error) { return gwtest.DefaultToken, nil }
func (r *renewing) Renew(context.Context) (string, error) {
	r.renews++
	return "cap_" + strings.Repeat("N", 43), nil
}

func TestNoResponseAndProtocolErrors(t *testing.T) {
	s := newSrv(t)
	cases := []struct {
		svc      string
		is       error
		attempts int
	}{
		{"noresp", gw.ErrUnavailable, 5}, // sin respuesta = Unavailable reintentable
		{"trunc", gw.ErrProtocol, 2},     // error de protocolo: una sola repetición
		{"badmagic", gw.ErrProtocol, 2},
	}
	for _, c := range cases {
		t.Run(c.svc, func(t *testing.T) {
			before := len(s.Seen())
			o, _ := opts(s)
			_, err := gw.Dial(context.Background(), s.URI(c.svc), o)
			if !errors.Is(err, c.is) {
				t.Fatalf("%v", err)
			}
			if n := len(s.Seen()) - before; n != c.attempts {
				t.Fatalf("intentos %d, se esperaba %d", n, c.attempts)
			}
		})
	}
	o, _ := opts(s)
	_, err := gw.Dial(context.Background(), s.URI("noresp"), o)
	if !gw.IsRetryable(err) {
		t.Fatal("sin respuesta debe ser reintentable")
	}
}

func TestTLSRequirements(t *testing.T) {
	for name, cfg := range map[string]gwtest.Config{
		"solo TLS 1.2": {TLS12Only: true}, "sin ALPN": {NoALPN: true}, "ALPN distinto": {WrongALPN: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := gwtest.New(t, cfg)
			o, _ := opts(s)
			_, err := gw.Dial(context.Background(), s.URI("echo"), o)
			if !errors.Is(err, gw.ErrTLS) || gw.IsRetryable(err) {
				t.Fatalf("se esperaba ErrTLS no reintentable: %v", err)
			}
			if len(s.Seen()) != 0 {
				t.Fatal("no debe enviarse el encabezado si TLS no cumple")
			}
		})
	}
	s := newSrv(t)
	o, _ := opts(s)
	o.RootCAs = nil // CA desconocida
	if _, err := gw.Dial(context.Background(), s.URI("echo"), o); !errors.Is(err, gw.ErrTLS) || len(s.Seen()) != 0 {
		t.Fatalf("CA desconocida: %v", err)
	}
	o, _ = opts(s)
	other := strings.Replace(s.URI("echo"), "sni=gw.example.test", "sni=otro.example.test", 1)
	if _, err := gw.Dial(context.Background(), other, o); !errors.Is(err, gw.ErrTLS) || len(s.Seen()) != 0 {
		t.Fatalf("nombre distinto al SNI: %v", err)
	}
}

func TestHalfClose(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	c, err := gw.DialConn(context.Background(), s.URI("halfclose"), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload := bytes.Repeat([]byte("x"), 100)
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c) // el servidor sigue enviando tras nuestro semicierre
	if err != nil || string(got) != "recibidos 100 bytes" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestBulk5MB(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	c, err := gw.DialConn(context.Background(), s.URI("echo"), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	data := make([]byte, 5<<20)
	for i := range data {
		data[i] = byte(i*131 + i>>8)
	}
	werr := make(chan error, 1)
	go func() {
		_, err := c.Write(data)
		if err == nil {
			err = c.CloseWrite()
		}
		werr <- err
	}()
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-werr; err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(data) || len(got) != len(data) {
		t.Fatalf("eco de 5 MB distinto: %d bytes", len(got))
	}
}

func TestServerFirstBannerAndProxyInfo(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	o.WantProxyInfo = true
	c, err := gw.DialConn(context.Background(), s.URI("banner"), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	line := make([]byte, 22)
	if _, err := io.ReadFull(c, line); err != nil || string(line) != "SSH-2.0-capsule-test\r\n" {
		t.Fatalf("%q %v", line, err)
	}
	ap, ok := c.ClientAddr()
	if !ok || !ap.Addr().IsLoopback() || ap.Port() == 0 {
		t.Fatalf("client_addr: %v %v", ap, ok)
	}
	if s.Seen()[0].Request.Flags != 1 {
		t.Fatal("want_proxy_info")
	}
}

func TestUncleanCloseIsNotCleanEOF(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	c, err := gw.DialConn(context.Background(), s.URI("unclean"), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = io.ReadAll(c)
	if !errors.Is(err, gw.ErrUnexpectedClose) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("un cierre TCP sin close_notify no es fin limpio: %v", err)
	}
	// y un cierre ordenado sí lo es
	c2, _ := gw.DialConn(context.Background(), s.URI("echo"), o)
	defer c2.Close()
	c2.CloseWrite()
	if b, err := io.ReadAll(c2); err != nil || len(b) != 0 {
		t.Fatalf("cierre ordenado: %q %v", b, err)
	}
}

func TestCancellation(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, err := gw.Dial(ctx, s.URI("hang"), o)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > 3*time.Second {
		t.Fatalf("plazo: %v (%v)", err, time.Since(t0))
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel2() }()
	t0 = time.Now()
	_, err = gw.Dial(ctx2, s.URI("hang"), o)
	if !errors.Is(err, context.Canceled) || time.Since(t0) > 3*time.Second {
		t.Fatalf("cancelación: %v", err)
	}
	// cancelación durante la espera entre reintentos
	ctx3, cancel3 := context.WithCancel(context.Background())
	o3 := &gw.Options{Token: gwtest.DefaultToken, RootCAs: s.CertPool(), Retry: &gw.RetryPolicy{Initial: time.Hour, Jitter: -1}}
	go func() { time.Sleep(150 * time.Millisecond); cancel3() }()
	t0 = time.Now()
	_, err = gw.Dial(ctx3, s.URI("err-0401"), o3)
	if !errors.Is(err, context.Canceled) || time.Since(t0) > 3*time.Second {
		t.Fatalf("cancelación en el retroceso: %v", err)
	}
	// contexto ya cancelado: no toca la red
	n := len(s.Seen())
	if _, err := gw.Dial(ctx3, s.URI("echo"), o); !errors.Is(err, context.Canceled) || len(s.Seen()) != n {
		t.Fatalf("contexto cancelado: %v", err)
	}
}

func TestFamilyAndHappyEyeballs(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	var nets []string
	o.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		nets = append(nets, network)
		return (&net.Dialer{}).DialContext(ctx, "tcp", s.Addr())
	}
	for _, f := range []string{"4", "6", "any"} {
		c, err := gw.Dial(context.Background(), s.URI("echo", "family="+f), o)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	if strings.Join(nets, ",") != "tcp4,tcp6,tcp" {
		t.Fatalf("redes: %v", nets)
	}

	// "localhost" resuelve a ::1 y 127.0.0.1; el servidor solo escucha en IPv4:
	// el cliente debe caer de ::1 a 127.0.0.1 (Happy Eyeballs).
	o2, _ := opts(s)
	u := fmt.Sprintf("capsule://proyecto/echo?gateway=localhost:%d&sni=localhost", s.Port())
	c, err := gw.Dial(context.Background(), u, o2)
	if err != nil {
		t.Fatalf("localhost: %v", err)
	}
	c.Close()
}

func TestIPv6Server(t *testing.T) {
	s := gwtest.New(t, gwtest.Config{Listen: "[::1]:0"})
	o, _ := opts(s)
	c, err := gw.DialConn(context.Background(), s.URI("echo", "family=6"), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("v6"))
	b := make([]byte, 2)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "v6" {
		t.Fatal(err)
	}
}

func TestWrongTransportFunctions(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	if _, err := gw.Dial(context.Background(), s.URI("echo", "transport=udp"), o); !errors.Is(err, gw.ErrWrongTransport) {
		t.Fatal(err)
	}
	if _, err := gw.DialUDP(context.Background(), s.URI("echo"), o); !errors.Is(err, gw.ErrWrongTransport) {
		t.Fatal(err)
	}
	var ue *gw.URIError
	if _, err := gw.Dial(context.Background(), "capsule://P/s", o); !errors.As(err, &ue) {
		t.Fatalf("URI inválido: %v", err)
	}
	if _, err := gw.Dial(context.Background(), "capsule://p/s?token=abc", o); !errors.As(err, &ue) || strings.Contains(err.Error(), "abc") {
		t.Fatalf("token en el URI: %v", err)
	}
}

// El token no sale en ningún error, formato ni mensaje, ni siquiera si el
// servidor lo repite en su msg.
func TestTokenNeverLeaks(t *testing.T) {
	s := newSrv(t)
	tok := gwtest.DefaultToken
	var all []string
	collect := func(v ...any) {
		for _, x := range v {
			all = append(all, fmt.Sprintf("%v|%+v|%#v|%s|%q", x, x, x, x, x))
		}
	}
	for _, svc := range []string{"leak", "err-0201", "err-0401", "err-0501", "err-0901", "trunc", "badmagic", "noresp"} {
		o, _ := opts(s)
		_, err := gw.Dial(context.Background(), s.URI(svc), o)
		collect(err)
		var ge *gw.GatewayError
		if svc == "leak" && errors.As(err, &ge) && !strings.Contains(ge.Message, "[token]") {
			t.Errorf("el msg del servidor debe llegar redactado: %q", ge.Message)
		}
	}
	o, _ := opts(s)
	collect(*o, o, gw.NewStaticToken(tok), &gw.Request{AuthType: 1, Auth: tok, Project: "a", Service: "b"}, gw.Request{Auth: tok})
	o.RootCAs = nil
	_, err := gw.Dial(context.Background(), s.URI("echo"), o)
	collect(err)
	bad := *o
	bad.Token = "zzz_" + tok[4:]
	_, err = gw.Dial(context.Background(), s.URI("echo"), &bad)
	collect(err)
	_, err = gw.EncodeRequest(gw.Request{Transport: 1, Project: "p", Service: "s", AuthType: 1, Auth: "cap_ñ" + tok})
	collect(err)
	_, err = gw.EncodeRequest(gw.Request{Transport: 1, Project: "P", Service: "s", AuthType: 1, Auth: tok})
	collect(err)
	for _, x := range all {
		if strings.Contains(x, tok) || strings.Contains(x, tok[4:]) {
			t.Fatalf("el token aparece en: %s", x)
		}
	}
	if len(all) < 10 {
		t.Fatal("pocas muestras")
	}
	// solo viaja en el campo auth: no está en SNI ni en el URI
	for _, sn := range s.Seen() {
		if strings.Contains(sn.SNI, tok) {
			t.Fatal("token en SNI")
		}
	}
}
