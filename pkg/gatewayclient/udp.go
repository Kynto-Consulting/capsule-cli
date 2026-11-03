package gatewayclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const udpQueueLen = 256

// Addr es la dirección de un servicio capsule:// (net.Addr).
type Addr struct{ route string }

func (a Addr) Network() string { return "capsule-udp" }
func (a Addr) String() string  { return "capsule://" + a.route }

// PacketConn es una sesión UDP sobre flujo (transport 2). Implementa
// net.PacketConn y net.Conn: cada Read/ReadFrom entrega un datagrama; cada
// Write/WriteTo envía uno. Los keepalives (len=0) se envían solos cada
// idle_timeout/3 y los del gateway se ignoran.
type PacketConn struct {
	conn *Conn
	uri  *URI
	addr Addr

	maxDatagram int
	idle        time.Duration

	wmu      sync.Mutex
	lastSend atomic.Int64

	rq      chan []byte
	done    chan struct{}
	once    sync.Once
	errMu   sync.Mutex
	rerr    error
	dropped atomic.Uint64

	rdl, wdl deadline
}

// DialUDP abre una sesión UDP sobre flujo hacia capsule://p/s?transport=udp.
func DialUDP(ctx context.Context, uri string, opts *Options) (*PacketConn, error) {
	u, err := parseWith(uri, opts)
	if err != nil {
		return nil, err
	}
	return DialUDPURI(ctx, u, opts)
}

// DialUDPURI es DialUDP desde un URI analizado.
func DialUDPURI(ctx context.Context, u *URI, opts *Options) (*PacketConn, error) {
	if u.Transport != "udp" {
		return nil, fmt.Errorf("%w: DialUDP requiere ?transport=udp", ErrWrongTransport)
	}
	c, err := dialRetry(ctx, u, opts, TransportUDPStream)
	if err != nil {
		return nil, err
	}
	p := &PacketConn{conn: c, uri: u, addr: Addr{u.Route()}, maxDatagram: MaxDatagramSize, idle: 60 * time.Second,
		rq: make(chan []byte, udpQueueLen), done: make(chan struct{})}
	t := c.resp.TLVs
	if t.MaxDatagram != nil && int(*t.MaxDatagram) > 0 && int(*t.MaxDatagram) < p.maxDatagram {
		p.maxDatagram = int(*t.MaxDatagram)
	}
	if t.IdleTimeout != nil && *t.IdleTimeout > 0 {
		p.idle = time.Duration(*t.IdleTimeout) * time.Second
	}
	p.lastSend.Store(time.Now().UnixNano())
	p.rdl.init()
	p.wdl.init()
	go p.readLoop()
	go p.keepalive()
	return p, nil
}

func (p *PacketConn) fail(err error) {
	p.errMu.Lock()
	if p.rerr == nil {
		p.rerr = err
	}
	p.errMu.Unlock()
	p.once.Do(func() {
		close(p.done)
		p.conn.Close()
	})
}

func (p *PacketConn) err() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if p.rerr == nil || errors.Is(p.rerr, io.EOF) {
		return net.ErrClosed
	}
	return p.rerr
}

func (p *PacketConn) readLoop() {
	var dec FrameDecoder
	buf := make([]byte, 32*1024)
	for {
		n, err := p.conn.Read(buf)
		if n > 0 {
			dgs, _, derr := dec.Feed(buf[:n])
			for _, d := range dgs {
				select {
				case p.rq <- d:
				default:
					p.dropped.Add(1) // sin contrapresión indefinida: se descarta con contador
				}
			}
			if derr != nil {
				p.fail(derr)
				return
			}
		}
		if err != nil {
			p.fail(err)
			return
		}
	}
}

func (p *PacketConn) keepalive() {
	iv := p.idle / 3
	if iv <= 0 {
		iv = time.Second
	}
	t := time.NewTicker(iv / 2)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
			if time.Since(time.Unix(0, p.lastSend.Load())) >= iv/2 {
				p.wmu.Lock()
				_, err := p.conn.Write([]byte{0, 0})
				p.wmu.Unlock()
				if err != nil {
					p.fail(err)
					return
				}
				p.lastSend.Store(time.Now().UnixNano())
			}
		}
	}
}

// Read entrega el siguiente datagrama (si p es menor, se trunca como en UDP).
func (p *PacketConn) Read(b []byte) (int, error) {
	n, _, err := p.ReadFrom(b)
	return n, err
}

// ReadFrom entrega el siguiente datagrama; addr es la dirección del servicio.
func (p *PacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case d := <-p.rq:
		return copy(b, d), p.addr, nil
	default:
	}
	select {
	case d := <-p.rq:
		return copy(b, d), p.addr, nil
	case <-p.done:
		select { // vaciar lo ya recibido
		case d := <-p.rq:
			return copy(b, d), p.addr, nil
		default:
		}
		return 0, nil, p.err()
	case <-p.rdl.wait():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

// Write envía un datagrama.
func (p *PacketConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, ErrEmptyDatagram
	}
	if len(b) > MaxDatagramSize || len(b) > p.maxDatagram {
		return 0, ErrDatagramTooBig
	}
	select {
	case <-p.done:
		return 0, p.err()
	default:
	}
	frame, _ := AppendFrame(make([]byte, 0, len(b)+2), b)
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if t := p.wdl.get(); !t.IsZero() {
		p.conn.SetWriteDeadline(t)
	} else {
		p.conn.SetWriteDeadline(time.Time{})
	}
	if _, err := p.conn.Conn.Write(frame); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return 0, err
		}
		p.fail(err)
		return 0, err
	}
	p.lastSend.Store(time.Now().UnixNano())
	return len(b), nil
}

// WriteTo envía un datagrama al servicio; addr se ignora (el destino lo fija la ruta).
func (p *PacketConn) WriteTo(b []byte, _ net.Addr) (int, error) { return p.Write(b) }

// Close termina la sesión (UDP sobre flujo no tiene half-close).
func (p *PacketConn) Close() error {
	p.fail(net.ErrClosed)
	return nil
}

func (p *PacketConn) LocalAddr() net.Addr  { return p.conn.LocalAddr() }
func (p *PacketConn) RemoteAddr() net.Addr { return p.addr }

func (p *PacketConn) SetDeadline(t time.Time) error {
	p.rdl.set(t)
	p.wdl.set(t)
	return nil
}
func (p *PacketConn) SetReadDeadline(t time.Time) error  { p.rdl.set(t); return nil }
func (p *PacketConn) SetWriteDeadline(t time.Time) error { p.wdl.set(t); return nil }

// SessionID es el identificador de sesión del gateway (8 bytes) para correlacionar con la auditoría.
func (p *PacketConn) SessionID() []byte { return append([]byte(nil), p.conn.resp.TLVs.SessionID...) }

// ClientAddr es la dirección que el gateway ve del cliente.
func (p *PacketConn) ClientAddr() (netip.AddrPort, bool) { return p.conn.ClientAddr() }

// MaxDatagram es el tamaño máximo de datagrama aceptado.
func (p *PacketConn) MaxDatagram() int { return p.maxDatagram }

// IdleTimeout es la inactividad tras la cual el gateway cierra la sesión.
func (p *PacketConn) IdleTimeout() time.Duration { return p.idle }

// Dropped cuenta los datagramas recibidos descartados por cola llena.
func (p *PacketConn) Dropped() uint64 { return p.dropped.Load() }

// deadline es un plazo reemplazable con canal de aviso.
type deadline struct {
	mu sync.Mutex
	t  time.Time
	ch chan struct{}
	tm *time.Timer
}

func (d *deadline) init() { d.ch = make(chan struct{}) }

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tm != nil {
		d.tm.Stop()
		d.tm = nil
	}
	d.t = t
	select {
	case <-d.ch:
		d.ch = make(chan struct{})
	default:
	}
	if t.IsZero() {
		return
	}
	ch := d.ch
	if dur := time.Until(t); dur <= 0 {
		close(ch)
	} else {
		d.tm = time.AfterFunc(dur, func() { close(ch) })
	}
}

func (d *deadline) get() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.t
}

func (d *deadline) wait() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ch
}
