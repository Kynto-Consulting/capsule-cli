package gatewayclient

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
)

// Transportes del campo transport de CAP1 (sección 5).
const (
	TransportStream    = 1 // tcp transparente
	TransportUDPStream = 2 // udp sobre flujo, len16
	TransportUDPQUIC   = 3 // futuro
)

// Tipos de credencial (auth_type).
const (
	AuthCap = 1 // cap_
	AuthCgt = 2 // cgt_
	AuthCgc = 3 // cgc_
	AuthCgs = 4 // cgs_
)

// FlagWantProxyInfo pide la TLV client_addr en la respuesta.
const FlagWantProxyInfo = 0x01

const (
	maxHdrLen  = 1024
	minHdrLen  = 17
	maxAuthLen = 512
	minAuthLen = 8
)

var authPrefix = map[byte]string{AuthCap: "cap_", AuthCgt: "cgt_", AuthCgc: "cgc_", AuthCgs: "cgs_"}

// AuthTypeOf devuelve el auth_type de un token según su prefijo.
func AuthTypeOf(token string) (byte, bool) {
	for t, p := range authPrefix {
		if len(token) > len(p) && token[:len(p)] == p {
			return t, true
		}
	}
	return 0, false
}

func isTokenChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-'
}

func validAuth(authType byte, auth string) bool {
	p, ok := authPrefix[authType]
	if !ok || len(auth) < minAuthLen || len(auth) > maxAuthLen || len(auth) < len(p) || auth[:len(p)] != p {
		return false
	}
	for i := 0; i < len(auth); i++ {
		if !isTokenChar(auth[i]) {
			return false
		}
	}
	return true
}

func validClientID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(isTokenChar(c) && c != '_') && c != '/' && c != '+' {
			return false
		}
	}
	return true
}

// Request es el encabezado CAP1.
type Request struct {
	Flags     byte
	Transport byte
	Project   string
	Service   string
	AuthType  byte
	Auth      string
	Nonce     []byte // nil o 16 bytes (TLV 0x01)
	ClientID  string // "" = sin TLV 0x02
	Padding   int    // bytes de relleno (TLV 0x80) al codificar; 0..255 (una TLV 0x80); mayor se rechaza
	// Resultado de la decodificación:
	IgnoredTLVs int
}

// String no incluye la credencial.
func (r Request) String() string {
	return fmt.Sprintf("CAP1{transport=%d %s/%s auth_type=%d}", r.Transport, r.Project, r.Service, r.AuthType)
}

// GoString evita filtrar la credencial con %#v.
func (r Request) GoString() string { return r.String() }

// Format evita filtrar la credencial con %+v / %v.
func (r Request) Format(f fmt.State, verb rune) { fmt.Fprint(f, r.String()) }

// EncodeRequest codifica CAP1 byte a byte según la sección 5.
func EncodeRequest(r Request) ([]byte, error) {
	switch r.Transport {
	case TransportStream, TransportUDPStream, TransportUDPQUIC:
	default:
		return nil, fmt.Errorf("%w: transport %d", ErrInvalidField, r.Transport)
	}
	if r.Flags&^FlagWantProxyInfo != 0 {
		return nil, fmt.Errorf("%w: banderas reservadas", ErrInvalidField)
	}
	if !isName(r.Project) || !isName(r.Service) {
		return nil, fmt.Errorf("%w: proyecto o servicio", ErrInvalidField)
	}
	if !validAuth(r.AuthType, r.Auth) {
		return nil, fmt.Errorf("%w: credencial", ErrBadToken)
	}
	if r.Nonce != nil && len(r.Nonce) != 16 {
		return nil, fmt.Errorf("%w: nonce", ErrInvalidField)
	}
	if r.Padding < 0 || r.Padding > 255 {
		return nil, fmt.Errorf("%w: relleno fuera de 0..255 bytes", ErrInvalidField)
	}
	if r.ClientID != "" && !validClientID(r.ClientID) {
		return nil, fmt.Errorf("%w: client_id", ErrInvalidField)
	}
	b := make([]byte, 8, 128)
	copy(b, "CAP1")
	b[4], b[5] = 1, r.Flags
	b = append(b, r.Transport, byte(len(r.Project)))
	b = append(b, r.Project...)
	b = append(b, byte(len(r.Service)))
	b = append(b, r.Service...)
	b = append(b, r.AuthType, byte(len(r.Auth)>>8), byte(len(r.Auth)))
	b = append(b, r.Auth...)
	if r.Nonce != nil {
		b = append(b, 0x01, 16)
		b = append(b, r.Nonce...)
	}
	if r.ClientID != "" {
		b = append(b, 0x02, byte(len(r.ClientID)))
		b = append(b, r.ClientID...)
	}
	if r.Padding > 0 {
		b = append(b, 0x80, byte(r.Padding))
		b = append(b, make([]byte, r.Padding)...)
	}
	if len(b) > maxHdrLen {
		return nil, fmt.Errorf("%w: encabezado de %d bytes (máx. %d)", ErrInvalidField, len(b), maxHdrLen)
	}
	binary.BigEndian.PutUint16(b[6:8], uint16(len(b)))
	return b, nil
}

func reqErr(code uint16) error { return &RequestError{Status: 1, Code: code} }

// DecodeRequest implementa el algoritmo de la sección 5 (lado gateway y
// pruebas). eof indica que el flujo terminó. Devuelve ErrIncomplete si faltan
// bytes con el flujo abierto, *RequestError con el status/code exacto, o la
// solicitud y los bytes consumidos (lo que sigue es dato de aplicación).
// Nunca reserva memoria proporcional a campos no validados.
func DecodeRequest(buf []byte, eof bool) (*Request, int, error) {
	if len(buf) < 8 {
		if eof {
			return nil, 0, reqErr(0x010A)
		}
		return nil, 0, ErrIncomplete
	}
	if !bytes.Equal(buf[:4], []byte("CAP1")) {
		return nil, 0, reqErr(0x0102)
	}
	if buf[4] != 1 {
		return nil, 0, reqErr(0x0101)
	}
	hl := int(binary.BigEndian.Uint16(buf[6:8]))
	if hl < minHdrLen || hl > maxHdrLen {
		return nil, 0, reqErr(0x0103)
	}
	if len(buf) < hl {
		if eof {
			return nil, 0, reqErr(0x010A)
		}
		return nil, 0, ErrIncomplete
	}
	h := buf[:hl]
	r := &Request{Flags: h[5], Transport: h[8]}
	if r.Flags&^FlagWantProxyInfo != 0 {
		return nil, 0, reqErr(0x0104)
	}
	if r.Transport < 1 || r.Transport > 3 {
		return nil, 0, reqErr(0x0109)
	}
	pos := 9
	// name: longitud, límite del encabezado y charset.
	readName := func() (string, error) {
		if pos >= hl {
			return "", reqErr(0x010B)
		}
		n := int(h[pos])
		pos++
		if n < 1 || n > 63 {
			return "", reqErr(0x0105)
		}
		if pos+n > hl {
			return "", reqErr(0x010B)
		}
		s := string(h[pos : pos+n])
		pos += n
		if !isName(s) {
			return "", reqErr(0x0105)
		}
		return s, nil
	}
	var err error
	if r.Project, err = readName(); err != nil {
		return nil, 0, err
	}
	if r.Service, err = readName(); err != nil {
		return nil, 0, err
	}
	if pos+3 > hl {
		return nil, 0, reqErr(0x010B)
	}
	r.AuthType = h[pos]
	al := int(binary.BigEndian.Uint16(h[pos+1 : pos+3]))
	pos += 3
	if r.AuthType < 1 || r.AuthType > 4 || al < minAuthLen || al > maxAuthLen {
		return nil, 0, reqErr(0x0106)
	}
	if pos+al > hl {
		return nil, 0, reqErr(0x010B)
	}
	r.Auth = string(h[pos : pos+al])
	pos += al
	if !validAuth(r.AuthType, r.Auth) {
		return nil, 0, reqErr(0x0106)
	}
	for pos < hl {
		if pos+2 > hl {
			return nil, 0, reqErr(0x0107)
		}
		t, l := h[pos], int(h[pos+1])
		pos += 2
		if pos+l > hl {
			return nil, 0, reqErr(0x0107)
		}
		v := h[pos : pos+l]
		pos += l
		switch {
		case t == 0x01:
			if l != 16 || r.Nonce != nil {
				return nil, 0, reqErr(0x0107)
			}
			r.Nonce = append([]byte(nil), v...)
		case t == 0x02:
			if r.ClientID != "" || !validClientID(string(v)) {
				return nil, 0, reqErr(0x0107)
			}
			r.ClientID = string(v)
		case t >= 0x80:
			r.IgnoredTLVs++
		default:
			return nil, 0, reqErr(0x0108)
		}
	}
	return r, hl, nil
}

// ---- CAPR ----

// ClientAddr es la TLV 0x04: dirección del cliente vista por el gateway.
type ClientAddr struct {
	Addr netip.Addr
	Port uint16
}

// ResponseTLVs son las TLV conocidas de la respuesta (nil = ausente).
type ResponseTLVs struct {
	MaxVersion  *uint8
	RetryAfter  *uint16
	SessionID   []byte // 8 bytes
	ClientAddr  *ClientAddr
	MaxDatagram *uint16
	IdleTimeout *uint16
}

// Response es CAPR.
type Response struct {
	Version byte
	Status  byte
	Code    uint16
	Msg     string
	TLVs    ResponseTLVs
}

func perr(kind, detail string) error { return &ProtocolError{Kind: kind, Detail: detail} }

// EncodeResponse codifica CAPR (la usan el servidor de referencia y las pruebas).
func EncodeResponse(r Response) ([]byte, error) {
	if len(r.Msg) > 120 {
		return nil, fmt.Errorf("%w: msg", ErrInvalidField)
	}
	for i := 0; i < len(r.Msg); i++ {
		if r.Msg[i] < 0x20 || r.Msg[i] > 0x7e {
			return nil, fmt.Errorf("%w: msg", ErrInvalidField)
		}
	}
	var ext []byte
	t := r.TLVs
	if t.MaxVersion != nil {
		ext = append(ext, 0x01, 1, *t.MaxVersion)
	}
	if t.RetryAfter != nil {
		ext = append(ext, 0x02, 2, byte(*t.RetryAfter>>8), byte(*t.RetryAfter))
	}
	if t.SessionID != nil {
		if len(t.SessionID) != 8 {
			return nil, fmt.Errorf("%w: session_id", ErrInvalidField)
		}
		ext = append(ext, 0x03, 8)
		ext = append(ext, t.SessionID...)
	}
	if t.ClientAddr != nil {
		a := t.ClientAddr.Addr.Unmap()
		raw := a.AsSlice()
		fam := byte(4)
		if a.Is6() {
			fam = 6
		}
		ext = append(ext, 0x04, byte(1+len(raw)+2), fam)
		ext = append(ext, raw...)
		ext = append(ext, byte(t.ClientAddr.Port>>8), byte(t.ClientAddr.Port))
	}
	if t.MaxDatagram != nil {
		ext = append(ext, 0x05, 2, byte(*t.MaxDatagram>>8), byte(*t.MaxDatagram))
	}
	if t.IdleTimeout != nil {
		ext = append(ext, 0x06, 2, byte(*t.IdleTimeout>>8), byte(*t.IdleTimeout))
	}
	if len(ext) > 256 {
		return nil, fmt.Errorf("%w: ext", ErrInvalidField)
	}
	b := []byte("CAPR")
	b = append(b, r.Version, r.Status, byte(r.Code>>8), byte(r.Code), byte(len(r.Msg)))
	b = append(b, r.Msg...)
	b = append(b, byte(len(ext)>>8), byte(len(ext)))
	b = append(b, ext...)
	return b, nil
}

// DecodeResponse decodifica CAPR (sección 6). Con el flujo abierto y faltando
// bytes devuelve ErrIncomplete; con eof devuelve *ProtocolError{truncated}.
// Un status o code desconocido se decodifica sin error.
func DecodeResponse(buf []byte, eof bool) (*Response, int, error) {
	short := func() (*Response, int, error) {
		if eof {
			return nil, 0, perr("truncated", "")
		}
		return nil, 0, ErrIncomplete
	}
	if len(buf) < 9 {
		return short()
	}
	if !bytes.Equal(buf[:4], []byte("CAPR")) {
		return nil, 0, perr("bad_magic", "")
	}
	if buf[4] == 0 {
		return nil, 0, perr("bad_version", "")
	}
	ml := int(buf[8])
	if ml > 120 {
		return nil, 0, perr("msg_too_long", "")
	}
	if len(buf) < 9+ml+2 {
		return short()
	}
	r := &Response{Version: buf[4], Status: buf[5], Code: binary.BigEndian.Uint16(buf[6:8])}
	msg := buf[9 : 9+ml]
	for _, c := range msg {
		if c < 0x20 || c > 0x7e {
			return nil, 0, perr("bad_msg", "")
		}
	}
	r.Msg = string(msg)
	el := int(binary.BigEndian.Uint16(buf[9+ml : 11+ml]))
	if el > 256 {
		return nil, 0, perr("ext_too_long", "")
	}
	total := 11 + ml + el
	if len(buf) < total {
		return short()
	}
	ext := buf[11+ml : total]
	seen := map[byte]bool{}
	for p := 0; p < len(ext); {
		if p+2 > len(ext) {
			return nil, 0, perr("bad_ext", "")
		}
		t, l := ext[p], int(ext[p+1])
		p += 2
		if p+l > len(ext) {
			return nil, 0, perr("bad_ext", "")
		}
		v := ext[p : p+l]
		p += l
		if t >= 0x01 && t <= 0x06 {
			if seen[t] {
				return nil, 0, perr("bad_ext", "TLV repetida")
			}
			seen[t] = true
		}
		switch t {
		case 0x01:
			if l != 1 {
				return nil, 0, perr("bad_ext", "")
			}
			x := v[0]
			r.TLVs.MaxVersion = &x
		case 0x02, 0x05, 0x06:
			if l != 2 {
				return nil, 0, perr("bad_ext", "")
			}
			x := binary.BigEndian.Uint16(v)
			switch t {
			case 0x02:
				r.TLVs.RetryAfter = &x
			case 0x05:
				r.TLVs.MaxDatagram = &x
			default:
				r.TLVs.IdleTimeout = &x
			}
		case 0x03:
			if l != 8 {
				return nil, 0, perr("bad_ext", "")
			}
			r.TLVs.SessionID = append([]byte(nil), v...)
		case 0x04:
			if (l != 7 || v[0] != 4) && (l != 19 || v[0] != 6) {
				return nil, 0, perr("bad_ext", "")
			}
			a, _ := netip.AddrFromSlice(v[1 : l-2])
			r.TLVs.ClientAddr = &ClientAddr{Addr: a, Port: binary.BigEndian.Uint16(v[l-2:])}
		}
	}
	return r, total, nil
}

// ---- Tramas UDP len16 (sección 10.3) ----

// MaxDatagramSize es el tope fijo de un datagrama en transport 2.
const MaxDatagramSize = 2048

// AppendFrame añade la trama [len16|datagrama] (len=0 es keepalive: pasar nil).
func AppendFrame(dst, datagram []byte) ([]byte, error) {
	if len(datagram) > MaxDatagramSize {
		return dst, ErrDatagramTooBig
	}
	dst = append(dst, byte(len(datagram)>>8), byte(len(datagram)))
	return append(dst, datagram...), nil
}

// FrameDecoder reensambla tramas len16 de un flujo.
type FrameDecoder struct{ buf []byte }

// Feed añade bytes y devuelve los datagramas completos y el número de keepalives.
// len > 2048 devuelve *ProtocolError{frame_too_large} (hay que cerrar la conexión).
func (d *FrameDecoder) Feed(p []byte) (datagrams [][]byte, keepalives int, err error) {
	d.buf = append(d.buf, p...)
	for len(d.buf) >= 2 {
		n := int(binary.BigEndian.Uint16(d.buf))
		if n > MaxDatagramSize {
			return datagrams, keepalives, perr("frame_too_large", "")
		}
		if n == 0 {
			keepalives++
			d.buf = d.buf[2:]
			continue
		}
		if len(d.buf) < 2+n {
			break
		}
		datagrams = append(datagrams, append([]byte(nil), d.buf[2:2+n]...))
		d.buf = d.buf[2+n:]
	}
	if len(d.buf) == 0 {
		d.buf = nil
	}
	return
}

// Pending son los bytes recibidos que aún no forman una trama completa.
func (d *FrameDecoder) Pending() int { return len(d.buf) }
