package gatewayclient

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// Tiempos por defecto (PROTOCOL.md sección 9.3).
const (
	DefaultConnectTimeout   = 10 * time.Second
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultResponseTimeout  = 20 * time.Second
)

// RetryPolicy es el retroceso exponencial de la sección 7: inicio 500 ms,
// factor 2, tope 30 s, jitter ±20 %, máximo 5 intentos, respetando retry_after.
// El valor cero usa esos defectos.
type RetryPolicy struct {
	MaxAttempts   int           // 0 = 5; 1 = sin reintentos
	Initial       time.Duration // 0 = 500 ms
	Max           time.Duration // 0 = 30 s
	Factor        float64       // 0 = 2
	Jitter        float64       // 0 = 0.2; negativo = sin jitter
	MaxRetryAfter time.Duration // 0 = 60 s: un retry_after mayor se devuelve como error en vez de esperar
	// Sleep espera d o devuelve el error del contexto (reemplazable en pruebas).
	Sleep func(ctx context.Context, d time.Duration) error
}

// NoRetry desactiva los reintentos automáticos.
func NoRetry() *RetryPolicy { return &RetryPolicy{MaxAttempts: 1} }

func (p *RetryPolicy) norm() RetryPolicy {
	var r RetryPolicy
	if p != nil {
		r = *p
	}
	if r.MaxAttempts <= 0 {
		r.MaxAttempts = 5
	}
	if r.Initial <= 0 {
		r.Initial = 500 * time.Millisecond
	}
	if r.Max <= 0 {
		r.Max = 30 * time.Second
	}
	if r.Factor <= 0 {
		r.Factor = 2
	}
	if r.Jitter == 0 {
		r.Jitter = 0.2
	} else if r.Jitter < 0 {
		r.Jitter = 0
	}
	if r.MaxRetryAfter <= 0 {
		r.MaxRetryAfter = 60 * time.Second
	}
	if r.Sleep == nil {
		r.Sleep = sleepCtx
	}
	return r
}

func (r RetryPolicy) backoff(attempt int) time.Duration {
	d := float64(r.Initial) * math.Pow(r.Factor, float64(attempt-1))
	if d > float64(r.Max) {
		d = float64(r.Max)
	}
	if r.Jitter > 0 {
		d *= 1 + (mrand.Float64()*2-1)*r.Jitter
	}
	return time.Duration(d)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Options configura Dial, DialUDP y Listen. El valor cero es válido: token
// del entorno (CAPSULE_GATEWAY_TOKEN / CAPSULE_TOKEN), zona por defecto y TLS
// contra las raíces del sistema.
type Options struct {
	Token       string      // credencial fija (cap_, cgt_, cgc_, cgs_)
	TokenSource TokenSource // prevalece sobre Token
	Zone        string      // zona por defecto (vacío = CAPSULE_GATEWAY_ZONE o la de fábrica)
	ClientID    string      // TLV client_id (vacío = "capsule-go/<versión>")
	// RootCAs fija la CA del gateway en despliegues privados. No existe opción
	// para desactivar la verificación del certificado.
	RootCAs       *x509.CertPool
	WantProxyInfo bool // pide la dirección que el gateway ve del cliente (Conn.ClientAddr)
	// DialContext reemplaza la conexión TCP (red de pruebas, proxies). Recibe
	// la dirección host:puerto derivada del URI.
	DialContext      func(ctx context.Context, network, addr string) (net.Conn, error)
	ConnectTimeout   time.Duration
	HandshakeTimeout time.Duration
	ResponseTimeout  time.Duration
	Retry            *RetryPolicy
	// OnError recibe los errores por conexión de un Listener (nunca el token).
	OnError func(error)
}

// String, GoString y Format omiten la credencial.
func (o Options) String() string {
	return fmt.Sprintf("gatewayclient.Options{zona=%q, token=[redactado]}", o.Zone)
}
func (o Options) GoString() string           { return o.String() }
func (o Options) Format(f fmt.State, _ rune) { fmt.Fprint(f, o.String()) }

func (o *Options) tokenSource() TokenSource {
	if o != nil {
		if o.TokenSource != nil {
			return o.TokenSource
		}
		if o.Token != "" {
			return NewStaticToken(o.Token)
		}
	}
	return EnvToken{}
}

func orDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Conn es una conexión transport 1 establecida: bytes transparentes con
// semicierre (CloseWrite) y close_notify real.
type Conn struct {
	*tls.Conn
	tr   *eofTracker
	resp *Response
	uri  *URI
}

// Response devuelve el CAPR recibido (status 0).
func (c *Conn) Response() *Response { return c.resp }

// URI devuelve el URI analizado con el que se abrió.
func (c *Conn) URI() *URI { return c.uri }

// ClientAddr devuelve la dirección que el gateway ve del cliente (si se pidió
// WantProxyInfo y el gateway la envió).
func (c *Conn) ClientAddr() (netip.AddrPort, bool) {
	if a := c.resp.TLVs.ClientAddr; a != nil {
		return netip.AddrPortFrom(a.Addr, a.Port), true
	}
	return netip.AddrPort{}, false
}

// Read lee bytes del destino. Un cierre TCP sin close_notify no es un fin
// limpio: devuelve un error que cumple errors.Is(err, ErrUnexpectedClose) e
// io.ErrUnexpectedEOF (crypto/tls lo reporta como io.EOF, por eso se vigila el
// conector subyacente).
func (c *Conn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err == io.EOF && c.tr.sawEOF.Load() {
		err = fmt.Errorf("%w: %w", ErrUnexpectedClose, io.ErrUnexpectedEOF)
	} else if errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, ErrUnexpectedClose) {
		err = fmt.Errorf("%w: %w", ErrUnexpectedClose, err)
	}
	return n, err
}

// eofTracker observa si el TCP terminó por debajo de TLS.
type eofTracker struct {
	net.Conn
	sawEOF atomic.Bool
}

func (t *eofTracker) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if err == io.EOF {
		t.sawEOF.Store(true)
	}
	return n, err
}

// Dial abre una conexión transport 1 (flujo TCP transparente) hacia
// capsule://proyecto/servicio. El valor devuelto es un *Conn. ctx gobierna el
// establecimiento (DNS, TCP, TLS, CAPR y los reintentos), no la vida posterior.
func Dial(ctx context.Context, uri string, opts *Options) (net.Conn, error) {
	c, err := DialConn(ctx, uri, opts)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// DialConn es Dial con el tipo concreto.
func DialConn(ctx context.Context, uri string, opts *Options) (*Conn, error) {
	u, err := parseWith(uri, opts)
	if err != nil {
		return nil, err
	}
	return DialURI(ctx, u, opts)
}

func parseWith(uri string, opts *Options) (*URI, error) {
	zone := ""
	if opts != nil {
		zone = opts.Zone
	}
	return ParseURI(uri, zone)
}

// DialURI abre la conexión desde un URI ya analizado.
func DialURI(ctx context.Context, u *URI, opts *Options) (*Conn, error) {
	if u.Transport != "tcp" {
		return nil, fmt.Errorf("%w: el URI pide transport=%s; use DialUDP", ErrWrongTransport, u.Transport)
	}
	c, err := dialRetry(ctx, u, opts, TransportStream)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func dialRetry(ctx context.Context, u *URI, o *Options, transport byte) (*Conn, error) {
	if o == nil {
		o = &Options{}
	}
	pol := o.Retry.norm()
	ts := o.tokenSource()
	var override string
	var renewed bool
	var n010A, n0603, nProto int
	for attempt := 1; ; attempt++ {
		token := override
		override = ""
		if token == "" {
			t, err := ts.Token(ctx)
			if err != nil {
				return nil, err
			}
			token = t
		}
		conn, err := dialOnce(ctx, u, o, transport, token)
		if err == nil {
			return conn, nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("conexión cancelada: %w", cerr)
		}
		var retryAfter time.Duration
		var ge *GatewayError
		var de *DialError
		var immediate bool
		switch {
		case errors.As(err, &ge):
			switch {
			case ge.Code == 0x0202:
				rs, ok := ts.(RenewableTokenSource)
				if !ok || renewed {
					return nil, wrapAttempts(err, attempt)
				}
				renewed = true
				t, rerr := rs.Renew(ctx)
				if rerr != nil {
					return nil, fmt.Errorf("renovando el token de conexión: %w", rerr)
				}
				override, immediate = t, true
			case ge.Code == 0x010A:
				n010A++
				if n010A > 1 {
					return nil, wrapAttempts(err, attempt)
				}
			case ge.Code == 0x0603:
				n0603++
				if n0603 > 2 {
					return nil, wrapAttempts(err, attempt)
				}
			case ge.Code == 0x0601 && attempt == 1:
				immediate = true
			case ge.Retryable():
			default:
				return nil, wrapAttempts(err, attempt)
			}
			retryAfter = ge.RetryAfter
		case errors.Is(err, ErrProtocol):
			nProto++
			if nProto > 1 {
				return nil, wrapAttempts(err, attempt)
			}
		case errors.As(err, &de) && de.retryable:
		default:
			return nil, wrapAttempts(err, attempt)
		}
		if attempt >= pol.MaxAttempts {
			return nil, wrapAttempts(err, attempt)
		}
		delay := pol.backoff(attempt)
		if immediate {
			delay = 0
		}
		if retryAfter > 0 {
			if retryAfter > pol.MaxRetryAfter {
				return nil, wrapAttempts(err, attempt)
			}
			if retryAfter > delay {
				delay = retryAfter
			}
		}
		if serr := pol.Sleep(ctx, delay); serr != nil {
			return nil, fmt.Errorf("conexión cancelada: %w", serr)
		}
	}
}

func wrapAttempts(err error, attempt int) error {
	if attempt <= 1 {
		return err
	}
	return fmt.Errorf("%w (tras %d intentos)", err, attempt)
}

func netDialError(phase string, err error) *DialError {
	return &DialError{Phase: phase, Err: err, kind: ErrUnavailable, retryable: transient(err)}
}

// transient: fallos de red que justifican reintentar.
func transient(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) && (oe.Op == "dial" || oe.Op == "read" || oe.Op == "write") {
		return true
	}
	return false
}

func dialOnce(ctx context.Context, u *URI, o *Options, transport byte, token string) (*Conn, error) {
	authType, ok := AuthTypeOf(token)
	if !ok || !validAuth(authType, token) {
		return nil, ErrBadToken
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generando nonce: %w", err)
	}
	cid := o.ClientID
	if cid == "" {
		cid = "capsule-go/" + Version
	}
	req := Request{Transport: transport, Project: u.Project, Service: u.Service, AuthType: authType, Auth: token, Nonce: nonce, ClientID: cid}
	if o.WantProxyInfo || transport == TransportUDPStream {
		req.Flags |= FlagWantProxyInfo
	}
	hdr, err := EncodeRequest(req)
	if err != nil {
		return nil, redactErr(err, token)
	}
	redact := []string{token}

	network := "tcp"
	switch u.Family {
	case "4":
		network = "tcp4"
	case "6":
		network = "tcp6"
	}
	addr := net.JoinHostPort(u.ConnectHost, strconv.Itoa(u.ConnectPort))
	var raw net.Conn
	if o.DialContext != nil {
		raw, err = o.DialContext(ctx, network, addr)
	} else {
		d := &net.Dialer{Timeout: orDur(o.ConnectTimeout, DefaultConnectTimeout), FallbackDelay: 250 * time.Millisecond}
		raw, err = d.DialContext(ctx, network, addr)
	}
	if err != nil {
		return nil, netDialError("red", err)
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	ok = false
	defer func() {
		if !ok {
			stop()
			raw.Close()
		}
	}()

	tr := &eofTracker{Conn: raw}
	tc := tls.Client(tr, &tls.Config{
		ServerName: u.SNI,
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{ALPN},
		RootCAs:    o.RootCAs,
	})
	hctx, cancel := context.WithTimeout(ctx, orDur(o.HandshakeTimeout, DefaultHandshakeTimeout))
	err = tc.HandshakeContext(hctx)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if transient(err) {
			return nil, netDialError("tls", err)
		}
		return nil, &DialError{Phase: "tls", Err: err, kind: ErrTLS}
	}
	st := tc.ConnectionState()
	if st.NegotiatedProtocol != ALPN || st.Version < tls.VersionTLS13 {
		return nil, &DialError{Phase: "tls", Err: errors.New("el gateway no negoció TLS 1.3 con ALPN capsule/1"), kind: ErrTLS}
	}

	respTimeout := orDur(o.ResponseTimeout, DefaultResponseTimeout)
	tc.SetWriteDeadline(time.Now().Add(respTimeout))
	if _, err := tc.Write(hdr); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, netDialError("respuesta", err)
	}
	tc.SetReadDeadline(time.Now().Add(respTimeout))
	resp, got, err := readResponse(tc)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var pe *ProtocolError
		if errors.As(err, &pe) && !(pe.Kind == "truncated" && got == 0) {
			return nil, err
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, &DialError{Phase: "respuesta", Err: fmt.Errorf("sin respuesta del gateway en %s", respTimeout), kind: ErrUnavailable, retryable: true}
		}
		// Sin respuesta (cierre o RST antes del CAPR): Unavailable reintentable.
		return nil, &DialError{Phase: "respuesta", Err: errors.New("el gateway cerró la conexión sin responder"), kind: ErrUnavailable, retryable: true}
	}
	tc.SetDeadline(time.Time{})
	if resp.Status != 0 {
		ge := newGatewayError(resp, redact)
		tc.SetWriteDeadline(time.Now().Add(2 * time.Second))
		tc.Close()
		ok = true // ya cerrada; evita doble cierre
		stop()
		return nil, ge
	}
	if resp.Version != 1 {
		return nil, perr("bad_version", "versión efectiva distinta de 1")
	}
	if !stop() {
		return nil, ctx.Err()
	}
	ok = true
	return &Conn{Conn: tc, tr: tr, resp: resp, uri: u}, nil
}

func redactErr(err error, token string) error {
	return errors.New(redactText(err.Error(), []string{token}))
}

// readResponse lee exactamente un CAPR del flujo sin consumir bytes de datos
// posteriores. got es el número de bytes leídos antes del error.
func readResponse(r io.Reader) (resp *Response, got int, err error) {
	buf := make([]byte, 0, 64)
	grow := func(total int) error {
		for len(buf) < total {
			tmp := make([]byte, total-len(buf))
			n, e := io.ReadFull(r, tmp)
			buf = append(buf, tmp[:n]...)
			if e != nil {
				if e == io.EOF || e == io.ErrUnexpectedEOF {
					return perr("truncated", "")
				}
				return e
			}
		}
		return nil
	}
	if err = grow(4); err != nil {
		return nil, len(buf), err
	}
	if string(buf[:4]) != "CAPR" {
		return nil, len(buf), perr("bad_magic", "")
	}
	if err = grow(9); err != nil {
		return nil, len(buf), err
	}
	for {
		resp, _, derr := DecodeResponse(buf, false)
		if derr == nil {
			return resp, len(buf), nil
		}
		if !errors.Is(derr, ErrIncomplete) {
			return nil, len(buf), derr
		}
		need := 9 + int(buf[8]) + 2
		if len(buf) >= need {
			need += int(buf[need-2])<<8 | int(buf[need-1])
		}
		if err = grow(need); err != nil {
			return nil, len(buf), err
		}
	}
}
