package gatewayclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// HalfCloseTimeout es la inactividad máxima tras el primer semicierre
// (PROTOCOL.md sección 9.3: 300 s) antes de cerrar ambos lados.
var HalfCloseTimeout = 300 * time.Second

type closeWriter interface{ CloseWrite() error }

func closeWrite(c io.Closer) {
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// Pipe copia en ambos sentidos entre a (local) y b (remoto) propagando el
// semicierre: el EOF de un lado se traduce en CloseWrite del otro y la copia
// inversa continúa hasta su propio EOF. Un error o la cancelación de ctx
// cierran ambos lados. Devuelve los bytes a->b y b->a y el primer error que no
// sea un fin limpio. Al volver, ambas conexiones quedan cerradas.
func Pipe(ctx context.Context, a, b net.Conn) (aToB, bToA int64, err error) {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		once sync.Once
		tm   *time.Timer
	)
	closeAll := func() { a.Close(); b.Close() }
	setErr := func(e error) {
		if e == nil {
			return
		}
		mu.Lock()
		if err == nil {
			err = e
		}
		mu.Unlock()
		closeAll()
	}
	firstDone := func() {
		once.Do(func() {
			if HalfCloseTimeout > 0 {
				mu.Lock()
				tm = time.AfterFunc(HalfCloseTimeout, closeAll)
				mu.Unlock()
			}
		})
	}
	stop := context.AfterFunc(ctx, closeAll)
	defer stop()
	copyDir := func(dst, src net.Conn, n *int64) {
		defer wg.Done()
		c, e := io.Copy(dst, src)
		*n = c
		if e != nil {
			setErr(e)
			return
		}
		closeWrite(dst)
		firstDone()
	}
	wg.Add(2)
	go copyDir(b, a, &aToB)
	go copyDir(a, b, &bToA)
	wg.Wait()
	mu.Lock()
	if tm != nil {
		tm.Stop()
	}
	mu.Unlock()
	closeAll()
	if ctx.Err() != nil {
		err = ctx.Err() // los errores de copia son consecuencia del cierre por cancelación
	}
	return
}

// Stdio conecta in/out (por ejemplo stdin/stdout de un ProxyCommand de SSH) con
// la conexión remota. El EOF de in se traduce en close_notify; la función
// vuelve cuando el destino termina de enviar (no espera a que in cierre, que en
// una consola no se puede interrumpir). La conexión queda cerrada al volver.
func Stdio(ctx context.Context, in io.Reader, out io.Writer, remote net.Conn) error {
	stop := context.AfterFunc(ctx, func() { remote.Close() })
	defer stop()
	upErr := make(chan error, 1)
	go func() {
		_, e := io.Copy(remote, in)
		if e == nil {
			closeWrite(remote)
		}
		upErr <- e
	}()
	_, derr := io.Copy(out, remote)
	if derr == nil {
		// Dar al lado de subida la oportunidad de terminar su error, sin esperar un in bloqueado.
		select {
		case e := <-upErr:
			if e != nil && !errors.Is(e, net.ErrClosed) {
				derr = e
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	remote.Close()
	if derr == nil && ctx.Err() != nil {
		derr = ctx.Err()
	}
	return derr
}

// Listener es un túnel local: acepta conexiones (TCP) o datagramas (UDP) en una
// dirección local y los reenvía al servicio capsule://. No abre ninguna
// conexión al gateway hasta que llega la primera conexión local.
type Listener struct {
	uri  *URI
	opts *Options
	ln   net.Listener
	pc   net.PacketConn

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	sessions map[string]*udpSession
}

// Listen reserva local (host:puerto; puerto 0 = elegido por el sistema) para
// reenviar a uri. Si el URI lleva transport=udp la escucha es UDP.
func Listen(local, uri string, opts *Options) (*Listener, error) {
	u, err := parseWith(uri, opts)
	if err != nil {
		return nil, err
	}
	l := &Listener{uri: u, opts: opts}
	if opts == nil {
		l.opts = &Options{}
	}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	if u.Transport == "udp" {
		l.pc, err = net.ListenPacket("udp", local)
		l.sessions = map[string]*udpSession{}
	} else {
		l.ln, err = net.Listen("tcp", local)
	}
	if err != nil {
		l.cancel()
		return nil, err
	}
	return l, nil
}

// Addr es la dirección local real (útil con puerto 0).
func (l *Listener) Addr() net.Addr {
	if l.ln != nil {
		return l.ln.Addr()
	}
	return l.pc.LocalAddr()
}

// URI es el servicio de destino.
func (l *Listener) URI() *URI { return l.uri }

func (l *Listener) report(err error) {
	if err != nil && l.opts.OnError != nil && l.ctx.Err() == nil {
		l.opts.OnError(err)
	}
}

// Close detiene la escucha y cierra las conexiones en curso.
func (l *Listener) Close() error {
	l.cancel()
	var err error
	if l.ln != nil {
		err = l.ln.Close()
	} else {
		err = l.pc.Close()
	}
	l.wg.Wait()
	return err
}

// Serve atiende hasta que se llame a Close o ctx termine; devuelve nil en esos casos.
func (l *Listener) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	if l.pc != nil {
		return l.serveUDP()
	}
	for {
		c, err := l.ln.Accept()
		if err != nil {
			if l.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				l.wg.Wait()
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.handleTCP(c)
		}()
	}
}

func (l *Listener) handleTCP(local net.Conn) {
	remote, err := DialURI(l.ctx, l.uri, l.opts)
	if err != nil {
		local.Close()
		l.report(err)
		return
	}
	if _, _, err := Pipe(l.ctx, local, remote); err != nil {
		l.report(err)
	}
}

// ---- UDP local ----

const (
	maxUDPSessions     = 16
	udpSessionIdleTime = 2 * time.Minute
)

type udpSession struct {
	pc      *PacketConn
	src     net.Addr
	lastUse time.Time
}

func (l *Listener) serveUDP() error {
	buf := make([]byte, 65536)
	l.wg.Add(1)
	go func() { // expiración de sesiones inactivas
		defer l.wg.Done()
		t := time.NewTicker(udpSessionIdleTime / 4)
		defer t.Stop()
		for {
			select {
			case <-l.ctx.Done():
				return
			case <-t.C:
				l.mu.Lock()
				for k, s := range l.sessions {
					if time.Since(s.lastUse) > udpSessionIdleTime {
						s.pc.Close()
						delete(l.sessions, k)
					}
				}
				l.mu.Unlock()
			}
		}
	}()
	for {
		n, src, err := l.pc.ReadFrom(buf)
		if err != nil {
			if l.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				l.mu.Lock()
				for _, s := range l.sessions {
					s.pc.Close()
				}
				l.mu.Unlock()
				l.wg.Wait()
				return nil
			}
			return err
		}
		if n == 0 || n > MaxDatagramSize {
			continue
		}
		s, err := l.session(src)
		if err != nil {
			l.report(err)
			continue
		}
		if _, err := s.pc.Write(buf[:n]); err != nil && !errors.Is(err, ErrDatagramTooBig) {
			l.dropSession(src.String(), s)
			l.report(err)
		}
	}
}

func (l *Listener) session(src net.Addr) (*udpSession, error) {
	key := src.String()
	l.mu.Lock()
	if s, ok := l.sessions[key]; ok {
		s.lastUse = time.Now()
		l.mu.Unlock()
		return s, nil
	}
	if len(l.sessions) >= maxUDPSessions {
		l.mu.Unlock()
		return nil, fmt.Errorf("demasiadas sesiones UDP locales simultáneas (máx. %d)", maxUDPSessions)
	}
	l.mu.Unlock()
	pc, err := DialUDPURI(l.ctx, l.uri, l.opts)
	if err != nil {
		return nil, err
	}
	s := &udpSession{pc: pc, src: src, lastUse: time.Now()}
	l.mu.Lock()
	l.sessions[key] = s
	l.mu.Unlock()
	l.wg.Add(1)
	go func() { // respuestas: del servicio a la dirección local de origen
		defer l.wg.Done()
		b := make([]byte, MaxDatagramSize)
		for {
			n, _, err := pc.ReadFrom(b)
			if err != nil {
				l.dropSession(key, s)
				return
			}
			l.mu.Lock()
			s.lastUse = time.Now()
			l.mu.Unlock()
			if _, err := l.pc.WriteTo(b[:n], src); err != nil {
				return
			}
		}
	}()
	return s, nil
}

func (l *Listener) dropSession(key string, s *udpSession) {
	l.mu.Lock()
	if l.sessions[key] == s {
		delete(l.sessions, key)
	}
	l.mu.Unlock()
	s.pc.Close()
}
