package gatewayclient

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedHexes(f *testing.F, prefix string) {
	dir := vectorsDir(f)
	files, _ := filepath.Glob(filepath.Join(dir, "hex", prefix+"*.hex"))
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if raw, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil {
			f.Add(raw, false)
			f.Add(raw, true)
		}
	}
}

// Invariantes del decodificador de solicitudes: sin pánico, consumo <= entrada,
// resultado en {incompleto, ok, error de la tabla}, y lo decodificado se
// vuelve a codificar a un encabezado equivalente.
func FuzzDecodeRequest(f *testing.F) {
	seedHexes(f, "req_")
	f.Fuzz(func(t *testing.T, in []byte, eof bool) {
		r, n, err := DecodeRequest(in, eof)
		if err != nil {
			var re *RequestError
			if errors.Is(err, ErrIncomplete) {
				if eof {
					t.Fatal("incompleto con eof")
				}
				return
			}
			if !errors.As(err, &re) || re.Status != 1 || re.Code < 0x0101 || re.Code > 0x010B {
				t.Fatalf("error fuera de la tabla: %v", err)
			}
			return
		}
		if n < minHdrLen || n > maxHdrLen || n > len(in) {
			t.Fatalf("consumed %d fuera de rango (entrada %d)", n, len(in))
		}
		_ = r.String()
		again, err := EncodeRequest(Request{Flags: r.Flags, Transport: r.Transport, Project: r.Project, Service: r.Service,
			AuthType: r.AuthType, Auth: r.Auth, Nonce: r.Nonce, ClientID: r.ClientID})
		if err != nil {
			t.Fatalf("lo decodificado no se puede codificar: %v", err)
		}
		r2, _, err := DecodeRequest(again, true)
		if err != nil || r2.Project != r.Project || r2.Auth != r.Auth || r2.Transport != r.Transport {
			t.Fatalf("ida y vuelta: %v", err)
		}
	})
}

func FuzzDecodeResponse(f *testing.F) {
	seedHexes(f, "resp_")
	f.Fuzz(func(t *testing.T, in []byte, eof bool) {
		r, n, err := DecodeResponse(in, eof)
		if err != nil {
			var pe *ProtocolError
			if !errors.Is(err, ErrIncomplete) && !errors.As(err, &pe) {
				t.Fatalf("error inesperado: %v", err)
			}
			return
		}
		if n > len(in) || n < 11 || r.Version == 0 || len(r.Msg) > 120 {
			t.Fatalf("resultado inválido n=%d", n)
		}
		out, err := EncodeResponse(*r)
		if err != nil {
			t.Fatal(err)
		}
		r2, n2, err := DecodeResponse(out, true)
		if err != nil || n2 != len(out) || r2.Code != r.Code || r2.Msg != r.Msg {
			t.Fatalf("ida y vuelta: %v", err)
		}
	})
}

func FuzzParseURI(f *testing.F) {
	var v struct{ Vectors []uriVec }
	loadVectors(f, "uri.json", &v)
	for _, x := range v.Vectors {
		f.Add(x.URI)
	}
	f.Fuzz(func(t *testing.T, s string) {
		u, err := ParseURI(s, "")
		if err != nil {
			var ue *URIError
			if !errors.As(err, &ue) {
				t.Fatalf("error de tipo inesperado: %v", err)
			}
			return
		}
		if !isName(u.Project) || !isName(u.Service) || u.ConnectPort < 1 || u.ConnectPort > 65535 || u.ConnectHost == "" || u.SNI == "" {
			t.Fatalf("URI aceptado con campos inválidos: %+v", u)
		}
		if strings.ContainsAny(u.ConnectHost, "[]") {
			t.Fatal("el host no lleva corchetes")
		}
	})
}

func FuzzFrameDecoder(f *testing.F) {
	for _, h := range []string{"0003616263", "0001aa00000001bb", "0001aa0005aabb", "ffff00", ""} {
		b, _ := hex.DecodeString(h)
		f.Add(b, 3)
	}
	f.Fuzz(func(t *testing.T, in []byte, chunk int) {
		if chunk < 1 {
			chunk = 1
		}
		var whole, parts FrameDecoder
		a, ka, ea := whole.Feed(in)
		var b [][]byte
		kb := 0
		var eb error
		for i := 0; i < len(in) && eb == nil; i += chunk {
			end := i + chunk
			if end > len(in) {
				end = len(in)
			}
			g, k, e := parts.Feed(in[i:end])
			b, kb, eb = append(b, g...), kb+k, e
		}
		if (ea == nil) != (eb == nil) {
			t.Fatal("el trozado cambia el error")
		}
		if ea == nil && (len(a) != len(b) || ka != kb || whole.Pending() != parts.Pending()) {
			t.Fatal("el trozado cambia el resultado")
		}
		for _, d := range a {
			if len(d) < 1 || len(d) > MaxDatagramSize {
				t.Fatal("datagrama fuera de rango")
			}
		}
	})
}
