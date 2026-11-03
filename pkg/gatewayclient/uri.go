package gatewayclient

import (
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// Nombres normativos de error del analizador (PROTOCOL.md sección 2.3).
const (
	URIBadScheme   = "bad_scheme"
	URIFragment    = "fragment"
	URIUserinfo    = "userinfo"
	URIPort        = "port"
	URIBadProject  = "bad_project"
	URIBadPath     = "bad_path"
	URIBadService  = "bad_service"
	URIBadParam    = "bad_param"
	URIDupParam    = "dup_param"
	URIUnknown     = "unknown_param"
	URIBadEscape   = "bad_escape"
	URIBadGateway  = "bad_gateway"
	URISNIRequired = "sni_required"
)

// URI es el resultado del análisis de capsule://proyecto/servicio[?consulta].
type URI struct {
	Project     string
	Service     string
	Transport   string // "tcp" | "udp"
	Family      string // "4" | "6" | "any"
	ConnectHost string // sin corchetes; IPv6 en forma canónica RFC 5952
	ConnectPort int
	SNI         string
	// HasExplicitGateway es true si el URI traía gateway= (no se usó la zona).
	HasExplicitGateway bool
}

// Route devuelve "proyecto/servicio".
func (u *URI) Route() string { return u.Project + "/" + u.Service }

// ResolveZone devuelve la zona efectiva: la indicada, CAPSULE_GATEWAY_ZONE o la de fábrica.
func ResolveZone(zone string) string {
	if zone != "" {
		return zone
	}
	if z := strings.TrimSpace(os.Getenv(ZoneEnv)); z != "" {
		return z
	}
	return DefaultZone
}

func uerr(kind, detail string) error { return &URIError{Kind: kind, Detail: detail} }

func isLC(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }

// isName implementa `name` (sección 1.6): 1..63 de [a-z0-9-], sin guion al inicio ni al final.
func isName(s string) bool {
	if len(s) < 1 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isLC(c) && c != '-' {
			return false
		}
	}
	return s[0] != '-' && s[len(s)-1] != '-'
}

// isDNSName implementa `dnsname`: etiquetas `name` separadas por '.', total <= 253, minúsculas.
func isDNSName(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !isName(l) {
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

// valueChar: unreserved / sub-delims / ":" / "[" / "]" (el '%' se trata aparte).
func valueChar(c byte) bool {
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	return strings.IndexByte("-._~!$&'()*+,;=:[]", c) >= 0
}

// decodeValue decodifica %XX una sola vez y exige ASCII visible (0x21..0x7E).
func decodeValue(v string) (string, error) {
	out := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '%' {
			if i+2 >= len(v) {
				return "", uerr(URIBadEscape, "secuencia %XX incompleta")
			}
			if !isHex(v[i+1]) || !isHex(v[i+2]) {
				return "", uerr(URIBadEscape, "secuencia %XX inválida")
			}
			c = unhex(v[i+1])<<4 | unhex(v[i+2])
			i += 2
		} else if !valueChar(c) {
			return "", uerr(URIBadParam, "carácter no permitido en un valor")
		}
		if c < 0x21 || c > 0x7e {
			return "", uerr(URIBadParam, "el valor debe ser ASCII visible")
		}
		out = append(out, c)
	}
	return string(out), nil
}

func parsePort(s string) (int, bool) {
	if s == "" || len(s) > 5 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, _ := strconv.Atoi(s)
	if n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}

// parseGateway: host[:puerto]; IPv6 solo entre corchetes y sin zona.
func parseGateway(v string) (host string, port int, isIP bool, ok bool) {
	port = 443
	if strings.HasPrefix(v, "[") {
		end := strings.IndexByte(v, ']')
		if end < 0 {
			return
		}
		inner, after := v[1:end], v[end+1:]
		if strings.ContainsRune(inner, '%') {
			return
		}
		a, err := netip.ParseAddr(inner)
		if err != nil || !a.Is6() {
			return
		}
		if after != "" {
			if after[0] != ':' {
				return
			}
			p, pok := parsePort(after[1:])
			if !pok {
				return
			}
			port = p
		}
		return a.String(), port, true, true
	}
	h := v
	switch strings.Count(v, ":") {
	case 0:
	case 1:
		i := strings.IndexByte(v, ':')
		h = v[:i]
		p, pok := parsePort(v[i+1:])
		if !pok {
			return
		}
		port = p
	default:
		return // IPv6 sin corchetes
	}
	if h == "" {
		return
	}
	numeric := true
	for i := 0; i < len(h); i++ {
		if (h[i] < '0' || h[i] > '9') && h[i] != '.' {
			numeric = false
			break
		}
	}
	if numeric {
		a, err := netip.ParseAddr(h)
		if err != nil || !a.Is4() {
			return
		}
		return a.String(), port, true, true
	}
	if !isDNSName(h) {
		return
	}
	return h, port, false, true
}

// ParseURI analiza un URI capsule:// de forma estricta (PROTOCOL.md sección 2).
// defaultZone vacío = CAPSULE_GATEWAY_ZONE o la zona de fábrica.
func ParseURI(raw, defaultZone string) (*URI, error) {
	const scheme = "capsule://"
	if len(raw) < len(scheme) || !strings.EqualFold(raw[:len(scheme)], scheme) {
		return nil, uerr(URIBadScheme, "se esperaba capsule://proyecto/servicio")
	}
	rest := raw[len(scheme):]
	if strings.Contains(rest, "#") {
		return nil, uerr(URIFragment, "no se admite fragmento")
	}
	query, hasQuery := "", false
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		query, rest, hasQuery = rest[i+1:], rest[:i], true
	}
	project, path, hasSlash := rest, "", false
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		project, path, hasSlash = rest[:i], rest[i+1:], true
	}
	if strings.Contains(project, "@") {
		return nil, uerr(URIUserinfo, "el URI no lleva credenciales; el token se toma del entorno")
	}
	if strings.Contains(project, ":") {
		return nil, uerr(URIPort, "el URI no lleva puerto")
	}
	if !isName(project) {
		return nil, uerr(URIBadProject, "el proyecto debe ser [a-z0-9-], 1 a 63, sin guion inicial/final")
	}
	if !hasSlash || strings.Contains(path, "/") {
		return nil, uerr(URIBadPath, "formato capsule://proyecto/servicio")
	}
	if !isName(path) {
		return nil, uerr(URIBadService, "el servicio debe ser [a-z0-9-], 1 a 63, sin guion inicial/final")
	}
	u := &URI{Project: project, Service: path, Transport: "tcp", Family: "any"}

	var transport, family, zone, gateway, sni string
	var haveZone, haveGateway, haveSNI bool
	if hasQuery {
		if query == "" {
			return nil, uerr(URIBadParam, "consulta vacía")
		}
		type kv struct{ k, v string }
		var params []kv
		seen := map[string]bool{}
		for _, p := range strings.Split(query, "&") {
			i := strings.IndexByte(p, '=')
			if i < 0 {
				return nil, uerr(URIBadParam, "parámetro sin '='")
			}
			k, v := p[:i], p[i+1:]
			if k == "" || v == "" {
				return nil, uerr(URIBadParam, "clave o valor vacío")
			}
			for j := 0; j < len(k); j++ {
				if !isLC(k[j]) && k[j] != '-' && k[j] != '_' {
					return nil, uerr(URIBadParam, "clave inválida")
				}
			}
			if seen[k] {
				return nil, uerr(URIDupParam, "parámetro repetido: "+k)
			}
			seen[k] = true
			switch k {
			case "transport", "family", "zone", "gateway", "sni":
			default:
				if !strings.HasPrefix(k, "x-") {
					return nil, uerr(URIUnknown, "parámetro desconocido: "+k)
				}
			}
			params = append(params, kv{k, v})
		}
		for _, p := range params {
			if strings.HasPrefix(p.k, "x-") {
				continue
			}
			val, err := decodeValue(p.v)
			if err != nil {
				return nil, err
			}
			switch p.k {
			case "transport":
				if val != "tcp" && val != "udp" {
					return nil, uerr(URIBadParam, "transport debe ser tcp o udp")
				}
				transport = val
			case "family":
				if val != "4" && val != "6" && val != "any" {
					return nil, uerr(URIBadParam, "family debe ser 4, 6 o any")
				}
				family = val
			case "zone":
				if !isDNSName(val) || !strings.Contains(val, ".") {
					return nil, uerr(URIBadParam, "zone debe ser un nombre DNS en minúsculas con al menos 2 etiquetas")
				}
				zone, haveZone = val, true
			case "gateway":
				gateway, haveGateway = val, true
			case "sni":
				if !isDNSName(val) {
					return nil, uerr(URIBadParam, "sni debe ser un nombre DNS en minúsculas")
				}
				sni, haveSNI = val, true
			}
		}
		if haveZone && haveGateway {
			return nil, uerr(URIBadParam, "gateway y zone son excluyentes")
		}
	}
	if transport != "" {
		u.Transport = transport
	}
	if family != "" {
		u.Family = family
	}
	if haveGateway {
		h, port, isIP, ok := parseGateway(gateway)
		if !ok {
			return nil, uerr(URIBadGateway, "gateway debe ser host[:puerto]; IPv6 entre corchetes y sin zona")
		}
		if isIP && !haveSNI {
			return nil, uerr(URISNIRequired, "con gateway por IP literal hace falta sni=")
		}
		u.ConnectHost, u.ConnectPort, u.HasExplicitGateway = h, port, true
	} else {
		z := zone
		if !haveZone {
			z = ResolveZone(defaultZone)
		}
		u.ConnectHost = u.Transport + "." + z
		u.ConnectPort = 443
	}
	u.SNI = u.ConnectHost
	if haveSNI {
		u.SNI = sni
	}
	return u, nil
}
