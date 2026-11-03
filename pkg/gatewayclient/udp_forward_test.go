package gatewayclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gw "github.com/kynto-consulting/capsule/cli/pkg/gatewayclient"
	"github.com/kynto-consulting/capsule/cli/pkg/gatewayclient/gwtest"
)

func TestUDPEchoAndMetadata(t *testing.T) {
	s := gwtest.New(t, gwtest.Config{IdleTimeout: 3})
	o, _ := opts(s)
	p, err := gw.DialUDP(context.Background(), s.URI("udp-echo", "transport=udp"), o)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var _ net.PacketConn = p
	var _ net.Conn = p
	if p.MaxDatagram() != 1472 || p.IdleTimeout() != 3*time.Second || len(p.SessionID()) != 8 {
		t.Fatalf("metadatos: %d %v %x", p.MaxDatagram(), p.IdleTimeout(), p.SessionID())
	}
	if ap, ok := p.ClientAddr(); !ok || !ap.Addr().IsLoopback() {
		t.Fatalf("client_addr %v", ap)
	}
	if sn := s.Seen()[0]; sn.Request.Transport != 2 || sn.Request.Flags != 1 {
		t.Fatalf("solicitud UDP: %v", sn.Request)
	}
	for _, size := range []int{1, 100, 1472} {
		msg := bytes.Repeat([]byte{byte(size)}, size)
		if _, err := p.WriteTo(msg, nil); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		p.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, addr, err := p.ReadFrom(buf)
		if err != nil || !bytes.Equal(buf[:n], msg) || addr.String() != "capsule://proyecto/udp-echo" {
			t.Fatalf("eco de %d: n=%d addr=%v err=%v", size, n, addr, err)
		}
	}
	// muchos datagramas seguidos conservan orden y contenido
	for i := 0; i < 50; i++ {
		p.Write([]byte{byte(i), 1, 2})
	}
	for i := 0; i < 50; i++ {
		buf := make([]byte, 16)
		p.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := p.Read(buf)
		if err != nil || n != 3 || buf[0] != byte(i) {
			t.Fatalf("datagrama %d: %v %v", i, buf[:n], err)
		}
	}
	// límites y plazos
	if _, err := p.Write(make([]byte, 1473)); !errors.Is(err, gw.ErrDatagramTooBig) {
		t.Fatalf("más que max_datagram: %v", err)
	}
	if _, err := p.Write(nil); !errors.Is(err, gw.ErrEmptyDatagram) {
		t.Fatal(err)
	}
	p.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := p.Read(make([]byte, 8)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("plazo de lectura: %v", err)
	}
	// keepalive cada idle_timeout/3 = 1 s sin tráfico
	p.SetReadDeadline(time.Time{})
	time.Sleep(2500 * time.Millisecond)
	if k := s.Keepalives(); k < 2 {
		t.Fatalf("keepalives recibidos: %d", k)
	}
	p.Close()
	if _, err := p.Read(make([]byte, 8)); err == nil {
		t.Fatal("lectura tras Close")
	}
	if _, err := p.Write([]byte("x")); err == nil {
		t.Fatal("escritura tras Close")
	}
}

func TestUDPErrorStatusAndCancel(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	if _, err := gw.DialUDP(context.Background(), s.URI("err-0504", "transport=udp"), o); !errors.Is(err, gw.ErrQuotaExceeded) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := gw.DialUDP(ctx, s.URI("hang", "transport=udp"), o); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestListenerTCPForward(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	var errs []error
	var mu sync.Mutex
	o.OnError = func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }
	l, err := gw.Listen("127.0.0.1:0", s.URI("halfclose"), o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Serve(context.Background()) }()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte(strings.Repeat("a", 1000)))
	c.(*net.TCPConn).CloseWrite() // el semicierre local llega al servicio
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "recibidos 1000 bytes" {
		t.Fatalf("%q %v", got, err)
	}
	c.Close()

	// 5 MB por el túnel, eco
	l2, err := gw.Listen("127.0.0.1:0", s.URI("echo"), o)
	if err != nil {
		t.Fatal(err)
	}
	go l2.Serve(context.Background())
	c2, _ := net.Dial("tcp", l2.Addr().String())
	data := bytes.Repeat([]byte("0123456789abcdef"), 5<<16)
	go func() { c2.Write(data); c2.(*net.TCPConn).CloseWrite() }()
	back, err := io.ReadAll(c2)
	if err != nil || !bytes.Equal(back, data) {
		t.Fatalf("eco por el túnel: %d bytes, %v", len(back), err)
	}
	c2.Close()
	l2.Close()

	// un fallo del gateway cierra la conexión local y se informa
	l3, _ := gw.Listen("127.0.0.1:0", s.URI("err-0201"), o)
	go l3.Serve(context.Background())
	c3, _ := net.Dial("tcp", l3.Addr().String())
	c3.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadAll(c3); err != nil && !strings.Contains(err.Error(), "reset") && !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("lectura local tras fallo: %v", err)
	}
	c3.Close()
	time.Sleep(100 * time.Millisecond)
	l3.Close()
	mu.Lock()
	if len(errs) == 0 || !errors.Is(errs[0], gw.ErrNotAuthorized) {
		t.Fatalf("OnError: %v", errs)
	}
	mu.Unlock()

	l.Close()
	if err := <-done; err != nil {
		t.Fatalf("Serve tras Close: %v", err)
	}
	if _, err := gw.Listen("127.0.0.1:0", "capsule://P/x", nil); err == nil {
		t.Fatal("URI inválido")
	}
}

func TestListenerUDPForward(t *testing.T) {
	s := newSrv(t)
	o, _ := opts(s)
	l, err := gw.Listen("127.0.0.1:0", s.URI("udp-echo", "transport=udp"), o)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go l.Serve(context.Background())
	for i := 0; i < 2; i++ { // dos orígenes locales = dos sesiones
		c, err := net.Dial("udp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		msg := []byte{byte(i), 9, 9, 9}
		c.Write(msg)
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 64)
		n, err := c.Read(buf)
		if err != nil || !bytes.Equal(buf[:n], msg) {
			t.Fatalf("origen %d: %v %v", i, buf[:n], err)
		}
		c.Close()
	}
	if n := len(s.Seen()); n != 2 {
		t.Fatalf("sesiones: %d", n)
	}
}

func TestPipeCancelAndStdio(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := gw.Pipe(ctx, a2, b1); done <- err }()
	go func() { a1.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(b2, buf); err != nil || string(buf) != "ping" {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Pipe no terminó con la cancelación")
	}

	s := newSrv(t)
	o, _ := opts(s)
	c, err := gw.Dial(context.Background(), s.URI("echo"), o)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := gw.Stdio(context.Background(), strings.NewReader("hola stdio"), &out, c); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hola stdio" {
		t.Fatalf("stdio: %q", out.String())
	}
}
