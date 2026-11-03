// Package gwtest es un servidor de referencia mínimo del protocolo capsule/1
// (TLS 1.3 con certificado autofirmado, CAP1/CAPR) para pruebas de clientes.
// No es el gateway de producción: no consulta ninguna base ni plano de control.
// El comportamiento lo fija el NOMBRE DEL SERVICIO pedido:
//
//	echo            responde OK y devuelve los bytes hasta el EOF del cliente
//	halfclose       lee hasta el EOF del cliente y entonces responde "recibidos N bytes" y cierra
//	banner          habla primero: tras el CAPR envía "SSH-2.0-capsule-test\r\n"
//	err-SSNN        responde estado 0xSS código 0xSSNN (hex de 4 dígitos), p. ej. err-0201
//	flaky-N         los N primeros intentos devuelven 0x0401; luego se comporta como echo
//	noresp          cierra sin responder
//	trunc           envía 5 bytes de CAPR y cierra el TCP
//	hang            no responde nunca
//	badmagic        responde con una magic incorrecta
//	unclean         tras el OK envía "hola" y cierra el TCP sin close_notify
//	leak            responde 4/0x0401 con el token dentro del msg (prueba de redacción)
//	udp-echo        (transport 2) devuelve cada datagrama; anuncia session_id, client_addr,
//	                max_datagram e idle_timeout; cuenta los keepalives recibidos
//
// La credencial debe estar en Server.Tokens y el proyecto debe ser Server.Project;
// si no, responde 2/0x0201 con msg "not authorized".
package gwtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gw "github.com/kynto-consulting/capsule/cli/pkg/gatewayclient"
)

// Config del servidor.
type Config struct {
	Listen      string   // "127.0.0.1:0" por defecto; "[::1]:0" para IPv6
	Project     string   // "proyecto" por defecto
	Tokens      []string // credenciales aceptadas (por defecto "cap_" + 43 x 'T')
	RetryAfter  uint16   // segundos en los estados 5 y 6 (0 = sin TLV)
	IdleTimeout uint16   // TLV idle_timeout del UDP (0 = 60)
	TLS12Only   bool     // solo acepta TLS 1.2 (para probar el rechazo de versiones bajas)
	NoALPN      bool     // no negocia ALPN
	WrongALPN   bool     // ofrece solo "otro/1"
}

// Seen es lo que el servidor leyó de una conexión.
type Seen struct {
	Request gw.Request
	SNI     string
	Raw     []byte // encabezado CAP1 tal cual
	ALPN    string
	TLSVer  uint16
}

// Server es el servidor de referencia.
type Server struct {
	cfg    Config
	ln     net.Listener
	pool   *x509.CertPool
	caPEM  []byte
	wg     sync.WaitGroup
	mu     sync.Mutex
	seen   []Seen
	flaky  map[string]int
	ka     atomic.Int64
	closed atomic.Bool
	// Hostname con el que el certificado es válido.
	Hostname string
}

// DefaultToken es la credencial aceptada si Config.Tokens está vacío.
var DefaultToken = "cap_" + strings.Repeat("T", 43)

// New arranca el servidor y lo cierra al terminar la prueba.
func New(tb testing.TB, cfg Config) *Server {
	tb.Helper()
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.Project == "" {
		cfg.Project = "proyecto"
	}
	if len(cfg.Tokens) == 0 {
		cfg.Tokens = []string{DefaultToken}
	}
	cert, pool, caPEM := makeCert(tb)
	tc := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{gw.ALPN}}
	if cfg.TLS12Only {
		tc.MinVersion, tc.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	}
	if cfg.NoALPN {
		tc.NextProtos = nil
	}
	if cfg.WrongALPN {
		tc.NextProtos = []string{"otro/1"}
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		tb.Skipf("no se pudo escuchar en %s: %v", cfg.Listen, err)
	}
	s := &Server{cfg: cfg, ln: ln, pool: pool, caPEM: caPEM, flaky: map[string]int{}, Hostname: "gw.example.test"}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(tls.Server(c, tc), c)
			}()
		}
	}()
	tb.Cleanup(s.Close)
	return s
}

func (s *Server) Close() {
	if s.closed.Swap(true) {
		return
	}
	s.ln.Close()
	s.wg.Wait()
}

// Addr devuelve "host:puerto" del socket.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Port devuelve el puerto.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// CertPool contiene la CA del servidor (para Options.RootCAs).
func (s *Server) CertPool() *x509.CertPool { return s.pool }

// CAPEM es la CA en PEM (para CAPSULE_GATEWAY_CA_FILE).
func (s *Server) CAPEM() []byte { return s.caPEM }

// URI arma capsule://proyecto/servicio?gateway=<ip>:<puerto>&sni=gw.example.test[&transport=udp].
func (s *Server) URI(service string, extra ...string) string {
	host := s.ln.Addr().String()
	if tcp := s.ln.Addr().(*net.TCPAddr); tcp.IP.To4() == nil {
		host = "[" + tcp.IP.String() + "]:" + strconv.Itoa(tcp.Port)
	}
	u := fmt.Sprintf("capsule://%s/%s?gateway=%s&sni=%s", s.cfg.Project, service, strings.NewReplacer("[", "%5B", "]", "%5D").Replace(host), s.Hostname)
	for _, e := range extra {
		u += "&" + e
	}
	return u
}

// Seen devuelve una copia de las solicitudes recibidas.
func (s *Server) Seen() []Seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Seen(nil), s.seen...)
}

// Keepalives devuelve los keepalives UDP recibidos.
func (s *Server) Keepalives() int { return int(s.ka.Load()) }

func (s *Server) respond(c net.Conn, r gw.Response) {
	b, err := gw.EncodeResponse(r)
	if err == nil {
		c.Write(b)
	}
}

func (s *Server) reject(c *tls.Conn, code uint16, msg string) {
	r := gw.Response{Version: 1, Status: byte(code >> 8), Code: code, Msg: msg}
	if (r.Status == 5 || r.Status == 6) && s.cfg.RetryAfter > 0 {
		ra := s.cfg.RetryAfter
		r.TLVs.RetryAfter = &ra
	}
	s.respond(c, r)
	c.Close()
}

func (s *Server) serve(c *tls.Conn, raw net.Conn) {
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(15 * time.Second))
	if err := c.Handshake(); err != nil {
		return
	}
	st := c.ConnectionState()
	var buf []byte
	tmp := make([]byte, 2048)
	var req *gw.Request
	var consumed int
	for {
		n, err := c.Read(tmp)
		buf = append(buf, tmp[:n]...)
		r, used, derr := gw.DecodeRequest(buf, err != nil)
		if derr == nil {
			req, consumed = r, used
			break
		}
		if re, ok := derr.(*gw.RequestError); ok {
			s.reject(c, re.Code, "bad request")
			return
		}
		if err != nil {
			return
		}
	}
	extra := buf[consumed:]
	s.mu.Lock()
	s.seen = append(s.seen, Seen{Request: *req, SNI: st.ServerName, Raw: append([]byte(nil), buf[:consumed]...), ALPN: st.NegotiatedProtocol, TLSVer: st.Version})
	s.mu.Unlock()
	raw.SetDeadline(time.Time{})

	okTok := false
	for _, t := range s.cfg.Tokens {
		if t == req.Auth {
			okTok = true
		}
	}
	if !okTok || req.Project != s.cfg.Project {
		time.Sleep(20 * time.Millisecond)
		s.reject(c, 0x0201, "not authorized")
		return
	}
	svc := req.Service
	switch {
	case strings.HasPrefix(svc, "err-"):
		v, err := strconv.ParseUint(svc[4:], 16, 16)
		if err != nil {
			s.reject(c, 0x0603, "internal error")
			return
		}
		s.reject(c, uint16(v), "gateway test status")
		return
	case svc == "leak":
		s.reject(c, 0x0401, "backend "+req.Auth+" down")
		return
	case svc == "noresp":
		return
	case svc == "trunc":
		c.Write([]byte("CAPR\x01"))
		raw.Close()
		return
	case svc == "hang":
		io.Copy(io.Discard, c)
		return
	case svc == "badmagic":
		c.Write([]byte("XXXX\x01\x00\x00\x00\x00\x00\x00"))
		c.Close()
		return
	case strings.HasPrefix(svc, "flaky-"):
		n, _ := strconv.Atoi(svc[6:])
		s.mu.Lock()
		s.flaky[svc]++
		cnt := s.flaky[svc]
		s.mu.Unlock()
		if cnt <= n {
			s.reject(c, 0x0401, "unavailable")
			return
		}
		svc = "echo"
	}
	if req.Transport == gw.TransportUDPStream {
		s.serveUDP(c, raw, req, extra)
		return
	}
	if req.Transport != gw.TransportStream {
		s.reject(c, 0x0109, "transport not enabled")
		return
	}
	r := gw.Response{Version: 1, Msg: "ok"}
	if req.Flags&gw.FlagWantProxyInfo != 0 {
		if ap, err := netip.ParseAddrPort(raw.RemoteAddr().String()); err == nil {
			r.TLVs.ClientAddr = &gw.ClientAddr{Addr: ap.Addr().Unmap(), Port: ap.Port()}
		}
	}
	s.respond(c, r)
	switch svc {
	case "banner":
		c.Write([]byte("SSH-2.0-capsule-test\r\n"))
		io.Copy(io.Discard, c)
		c.Close()
	case "unclean":
		c.Write([]byte("hola"))
		raw.Close()
	case "halfclose":
		n, _ := io.Copy(io.Discard, io.MultiReader(strings.NewReader(string(extra)), c))
		fmt.Fprintf(c, "recibidos %d bytes", n)
		c.Close()
	default: // echo
		if len(extra) > 0 {
			c.Write(extra)
		}
		io.Copy(c, c)
		c.CloseWrite()
		io.Copy(io.Discard, c)
		c.Close()
	}
}

func (s *Server) serveUDP(c *tls.Conn, raw net.Conn, req *gw.Request, extra []byte) {
	idle := s.cfg.IdleTimeout
	if idle == 0 {
		idle = 60
	}
	md := uint16(1472)
	sid := make([]byte, 8)
	rand.Read(sid)
	r := gw.Response{Version: 1, Msg: "ok", TLVs: gw.ResponseTLVs{SessionID: sid, MaxDatagram: &md, IdleTimeout: &idle}}
	if ap, err := netip.ParseAddrPort(raw.RemoteAddr().String()); err == nil {
		r.TLVs.ClientAddr = &gw.ClientAddr{Addr: ap.Addr().Unmap(), Port: ap.Port()}
	}
	s.respond(c, r)
	var dec gw.FrameDecoder
	handle := func(p []byte) bool {
		dgs, ka, err := dec.Feed(p)
		s.ka.Add(int64(ka))
		for _, d := range dgs {
			f, _ := gw.AppendFrame(nil, d)
			c.Write(f)
		}
		return err == nil
	}
	if len(extra) > 0 && !handle(extra) {
		return
	}
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if n > 0 && !handle(buf[:n]) {
			c.Close()
			return
		}
		if err != nil {
			c.Close()
			return
		}
	}
}

func makeCert(tb testing.TB) (tls.Certificate, *x509.CertPool, []byte) {
	tb.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "capsule gwtest CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		tb.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "gw.example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"gw.example.test", "tcp.apps.example.test", "udp.apps.example.test", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		tb.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}
