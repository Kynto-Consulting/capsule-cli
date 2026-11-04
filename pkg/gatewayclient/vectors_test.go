package gatewayclient

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Los vectores viven en docs/gateway/testvectors (norma). CAPSULE_TESTVECTORS
// permite apuntar a otra copia.
func vectorsDir(t testing.TB) string {
	if d := os.Getenv("CAPSULE_TESTVECTORS"); d != "" {
		return d
	}
	d, err := filepath.Abs(filepath.Join("..", "..", "..", "docs", "gateway", "testvectors"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "request.json")); err != nil {
		t.Fatalf("no se encuentran los vectores en %s (defina CAPSULE_TESTVECTORS): %v", d, err)
	}
	return d
}

func loadVectors(t testing.TB, name string, into any) {
	b, err := os.ReadFile(filepath.Join(vectorsDir(t), name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatal(err)
	}
}

func mustHex(t testing.TB, s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type uriVec struct {
	Name        string `json:"name"`
	URI         string `json:"uri"`
	DefaultZone string `json:"default_zone"`
	Expect      struct {
		OK     bool           `json:"ok"`
		Error  string         `json:"error"`
		Fields map[string]any `json:"fields"`
	} `json:"expect"`
}

func TestVectorsURI(t *testing.T) {
	var f struct{ Vectors []uriVec }
	loadVectors(t, "uri.json", &f)
	if len(f.Vectors) == 0 {
		t.Fatal("sin vectores")
	}
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			u, err := ParseURI(v.URI, v.DefaultZone)
			if !v.Expect.OK {
				var ue *URIError
				if !errors.As(err, &ue) {
					t.Fatalf("se esperaba URIError %s, salió %v", v.Expect.Error, err)
				}
				if ue.Kind != v.Expect.Error {
					t.Fatalf("error %s, se esperaba %s", ue.Kind, v.Expect.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]any{"project": u.Project, "service": u.Service, "transport": u.Transport, "family": u.Family,
				"connect_host": u.ConnectHost, "connect_port": float64(u.ConnectPort), "sni": u.SNI}
			for k, want := range v.Expect.Fields {
				if got[k] != want {
					t.Errorf("%s = %v, se esperaba %v", k, got[k], want)
				}
			}
		})
	}
}

type reqVec struct {
	Name      string `json:"name"`
	Hex       string `json:"hex"`
	EOF       bool   `json:"eof"`
	Canonical bool   `json:"canonical"`
	Expect    struct {
		Incomplete bool   `json:"incomplete"`
		OK         *bool  `json:"ok"`
		Consumed   int    `json:"consumed"`
		Status     int    `json:"status"`
		Code       string `json:"code"`
		Fields     struct {
			Transport   int    `json:"transport"`
			Flags       int    `json:"flags"`
			Project     string `json:"project"`
			Service     string `json:"service"`
			AuthType    int    `json:"auth_type"`
			Auth        string `json:"auth"`
			IgnoredTLVs int    `json:"ignored_tlvs"`
			NonceHex    string `json:"nonce_hex"`
			ClientID    string `json:"client_id"`
		} `json:"fields"`
	} `json:"expect"`
}

func TestVectorsRequest(t *testing.T) {
	var f struct{ Vectors []reqVec }
	loadVectors(t, "request.json", &f)
	if len(f.Vectors) < 60 {
		t.Fatalf("pocos vectores: %d", len(f.Vectors))
	}
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			in := mustHex(t, v.Hex)
			check := func(in []byte) {
				r, n, err := DecodeRequest(in, v.EOF)
				switch {
				case v.Expect.Incomplete:
					if !errors.Is(err, ErrIncomplete) {
						t.Fatalf("se esperaba incompleto, salió %v", err)
					}
				case v.Expect.OK != nil && !*v.Expect.OK:
					var re *RequestError
					if !errors.As(err, &re) {
						t.Fatalf("se esperaba RequestError %s, salió %v", v.Expect.Code, err)
					}
					if int(re.Status) != v.Expect.Status || fmt.Sprintf("0x%04x", re.Code) != strings.ToLower(v.Expect.Code) {
						t.Fatalf("status/code %d/0x%04x, se esperaba %d/%s", re.Status, re.Code, v.Expect.Status, v.Expect.Code)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
					e := v.Expect.Fields
					if n != v.Expect.Consumed || int(r.Transport) != e.Transport || int(r.Flags) != e.Flags || r.Project != e.Project ||
						r.Service != e.Service || int(r.AuthType) != e.AuthType || r.Auth != e.Auth || r.IgnoredTLVs != e.IgnoredTLVs ||
						hex.EncodeToString(r.Nonce) != e.NonceHex || r.ClientID != e.ClientID {
						t.Fatalf("campos distintos: consumed=%d vs %+v", n, v.Expect)
					}
				}
			}
			check(in)
			if v.Expect.OK != nil && *v.Expect.OK { // con bytes de más al final el resultado no cambia
				check(append(append([]byte(nil), in...), []byte("datos de aplicacion\x00\xff")...))
			}
			if v.Canonical && v.Expect.OK != nil && *v.Expect.OK {
				e := v.Expect.Fields
				req := Request{Flags: byte(e.Flags), Transport: byte(e.Transport), Project: e.Project, Service: e.Service,
					AuthType: byte(e.AuthType), Auth: e.Auth, ClientID: e.ClientID}
				if e.NonceHex != "" {
					req.Nonce = mustHex(t, e.NonceHex)
				}
				got, err := EncodeRequest(req)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, in) {
					t.Fatalf("codificación distinta:\n got %x\nwant %x", got, in)
				}
			}
		})
	}
}

type respVec struct {
	Name      string `json:"name"`
	Hex       string `json:"hex"`
	EOF       bool   `json:"eof"`
	Canonical bool   `json:"canonical"`
	Expect    struct {
		Incomplete bool   `json:"incomplete"`
		OK         *bool  `json:"ok"`
		Error      string `json:"error"`
		Consumed   int    `json:"consumed"`
		Fields     struct {
			Version int    `json:"version"`
			Status  int    `json:"status"`
			Code    int    `json:"code"`
			Msg     string `json:"msg"`
			TLVs    struct {
				MaxVersion   *int    `json:"max_version"`
				RetryAfter   *int    `json:"retry_after"`
				SessionIDHex *string `json:"session_id_hex"`
				ClientAddr   *struct {
					IP   string `json:"ip"`
					Port int    `json:"port"`
				} `json:"client_addr"`
				MaxDatagram *int `json:"max_datagram"`
				IdleTimeout *int `json:"idle_timeout"`
			} `json:"tlvs"`
		} `json:"fields"`
	} `json:"expect"`
}

func eqPtr[T ~uint8 | ~uint16](a *T, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return int(*a) == *b
}

func TestVectorsResponse(t *testing.T) {
	var f struct{ Vectors []respVec }
	loadVectors(t, "response.json", &f)
	if len(f.Vectors) < 25 {
		t.Fatalf("pocos vectores: %d", len(f.Vectors))
	}
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			in := mustHex(t, v.Hex)
			r, n, err := DecodeResponse(in, v.EOF)
			switch {
			case v.Expect.Incomplete:
				if !errors.Is(err, ErrIncomplete) {
					t.Fatalf("se esperaba incompleto, salió %v", err)
				}
			case v.Expect.OK != nil && !*v.Expect.OK:
				var pe *ProtocolError
				if !errors.As(err, &pe) || pe.Kind != v.Expect.Error {
					t.Fatalf("se esperaba %s, salió %v", v.Expect.Error, err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				e := v.Expect.Fields
				if n != v.Expect.Consumed || int(r.Version) != e.Version || int(r.Status) != e.Status || int(r.Code) != e.Code || r.Msg != e.Msg {
					t.Fatalf("campos distintos: %+v n=%d vs %+v", r, n, v.Expect)
				}
				tl := e.TLVs
				if !eqPtr(r.TLVs.MaxVersion, tl.MaxVersion) || !eqPtr(r.TLVs.RetryAfter, tl.RetryAfter) ||
					!eqPtr(r.TLVs.MaxDatagram, tl.MaxDatagram) || !eqPtr(r.TLVs.IdleTimeout, tl.IdleTimeout) {
					t.Fatalf("TLV distintas: %+v vs %+v", r.TLVs, tl)
				}
				if (tl.SessionIDHex == nil) != (r.TLVs.SessionID == nil) || (tl.SessionIDHex != nil && hex.EncodeToString(r.TLVs.SessionID) != *tl.SessionIDHex) {
					t.Fatalf("session_id distinto")
				}
				if (tl.ClientAddr == nil) != (r.TLVs.ClientAddr == nil) {
					t.Fatalf("client_addr distinto")
				}
				if tl.ClientAddr != nil {
					want := netip.MustParseAddr(tl.ClientAddr.IP)
					if r.TLVs.ClientAddr.Addr != want || int(r.TLVs.ClientAddr.Port) != tl.ClientAddr.Port {
						t.Fatalf("client_addr %v vs %+v", r.TLVs.ClientAddr, tl.ClientAddr)
					}
				}
				if v.Canonical {
					got, err := EncodeResponse(*r)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, in[:n]) {
						t.Fatalf("codificación distinta:\n got %x\nwant %x", got, in[:n])
					}
				}
				// El decodificador en flujo no consume bytes de datos posteriores.
				resp, got, err := readResponse(bytes.NewReader(append(append([]byte(nil), in...), 0xAA, 0xBB)))
				if err != nil || got != n || resp.Code != r.Code {
					t.Fatalf("readResponse: got=%d n=%d err=%v", got, n, err)
				}
			}
		})
	}
}

func TestVectorsUDPFrames(t *testing.T) {
	var f struct {
		Vectors []struct {
			Name   string `json:"name"`
			Hex    string `json:"hex"`
			Expect struct {
				Datagrams  []string `json:"datagrams"`
				Keepalives int      `json:"keepalives"`
				Pending    int      `json:"pending"`
				Error      string   `json:"error"`
			} `json:"expect"`
		}
	}
	loadVectors(t, "udp_frames.json", &f)
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			in := mustHex(t, v.Hex)
			// Alimentar de golpe y en trozos pequeños debe dar lo mismo.
			for _, chunk := range []int{len(in) + 1, 1, 3} {
				var d FrameDecoder
				var dgs [][]byte
				ka := 0
				var err error
				for i := 0; i < len(in) && err == nil; i += chunk {
					end := i + chunk
					if end > len(in) {
						end = len(in)
					}
					var g [][]byte
					var k int
					g, k, err = d.Feed(in[i:end])
					dgs = append(dgs, g...)
					ka += k
				}
				if v.Expect.Error != "" {
					var pe *ProtocolError
					if !errors.As(err, &pe) || pe.Kind != v.Expect.Error {
						t.Fatalf("chunk %d: se esperaba %s, salió %v", chunk, v.Expect.Error, err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(dgs) != len(v.Expect.Datagrams) || ka != v.Expect.Keepalives {
					t.Fatalf("chunk %d: %d datagramas, %d keepalives", chunk, len(dgs), ka)
				}
				for i, h := range v.Expect.Datagrams {
					if hex.EncodeToString(dgs[i]) != h {
						t.Fatalf("datagrama %d distinto", i)
					}
				}
				if d.Pending() != v.Expect.Pending {
					t.Fatalf("chunk %d: pending %d, se esperaba %d", chunk, d.Pending(), v.Expect.Pending)
				}
			}
			// El codificador reproduce los datagramas válidos.
			if v.Expect.Error == "" && v.Expect.Pending == 0 && v.Expect.Keepalives == 0 && len(v.Expect.Datagrams) > 0 {
				var out []byte
				for _, h := range v.Expect.Datagrams {
					var err error
					if out, err = AppendFrame(out, mustHex(t, h)); err != nil {
						t.Fatal(err)
					}
				}
				if !bytes.Equal(out, in) {
					t.Fatalf("AppendFrame distinto")
				}
			}
		})
	}
	if _, err := AppendFrame(nil, make([]byte, 2049)); !errors.Is(err, ErrDatagramTooBig) {
		t.Fatal("2049 debe rechazarse")
	}
}

func TestVectorsIPAllowlist(t *testing.T) {
	var f struct {
		Vectors []struct {
			Name    string   `json:"name"`
			CIDRs   []string `json:"cidrs"`
			IP      string   `json:"ip"`
			Allowed bool     `json:"allowed"`
		}
	}
	loadVectors(t, "ip_allowlist.json", &f)
	if len(f.Vectors) == 0 {
		t.Fatal("sin vectores")
	}
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			if got := IPInAllowlist(v.CIDRs, v.IP); got != v.Allowed {
				t.Fatalf("IPInAllowlist = %v, se esperaba %v", got, v.Allowed)
			}
		})
	}
}

// Los .hex y .bin de cada vector coinciden byte a byte con request.json/response.json.
func TestVectorsHexBinFiles(t *testing.T) {
	dir := vectorsDir(t)
	count := 0
	for _, file := range []string{"request.json", "response.json"} {
		var f struct{ Vectors []struct{ Name, Hex string } }
		loadVectors(t, file, &f)
		prefix := "req_"
		if file == "response.json" {
			prefix = "resp_"
		}
		for _, v := range f.Vectors {
			hx, err := os.ReadFile(filepath.Join(dir, "hex", prefix+v.Name+".hex"))
			if err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			if strings.TrimSpace(string(hx)) != v.Hex {
				t.Errorf("%s: .hex distinto del JSON", v.Name)
			}
			bn, err := os.ReadFile(filepath.Join(dir, "bin", prefix+v.Name+".bin"))
			if err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			if !bytes.Equal(bn, mustHex(t, v.Hex)) {
				t.Errorf("%s: .bin distinto del JSON", v.Name)
			}
			count++
		}
	}
	t.Logf("%d vectores con .hex y .bin verificados", count)
}

func TestEncodePaddingLimit(t *testing.T) {
	base := Request{Transport: 1, Project: "p", Service: "s", AuthType: 1, Auth: "cap_" + strings.Repeat("A", 40)}
	for _, n := range []int{-1, 256, 1000} {
		r := base
		r.Padding = n
		if _, err := EncodeRequest(r); !errors.Is(err, ErrInvalidField) {
			t.Fatalf("relleno %d debe rechazarse: %v", n, err)
		}
	}
	r := base
	r.Padding = 255
	b, err := EncodeRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if d, n, err := DecodeRequest(b, true); err != nil || n != len(b) || d.IgnoredTLVs != 1 {
		t.Fatal(err)
	}
}
