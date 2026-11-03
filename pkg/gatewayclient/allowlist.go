package gatewayclient

import "net/netip"

// IPInAllowlist aplica la regla de la sección 12.1 (lista de IP por familia,
// IPv4 mapeada convertida a IPv4, sin zona IPv6; lista vacía deniega todo).
// Lo usa el CLI para avisar de antemano y las pruebas con ip_allowlist.json;
// la decisión real siempre la toma el gateway con la IP de la conexión TCP.
func IPInAllowlist(cidrs []string, ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Zone() != "" {
		return false
	}
	a = a.Unmap()
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		if p.Addr().Is4() != a.Is4() {
			continue
		}
		if p.Contains(a) {
			return true
		}
	}
	return false
}
