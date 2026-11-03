// Package gatewayclient es el cliente Go del protocolo capsule/1 del gateway
// de Capsule (docs/gateway/PROTOCOL.md, norma). Reutilizable: lo usan el CLI
// (`capsule connect`, `capsule tunnel`, `capsule ssh`) y cualquier programa Go.
//
// Ubicación: cli/pkg/gatewayclient dentro del módulo del CLI
// (github.com/kynto-consulting/capsule/cli). Se eligió pkg/ y no internal/
// porque las reglas de internal/ solo dejan importarlo desde dentro de cli/,
// y el objetivo es un paquete reutilizable. La API pública estable es la
// descrita en este comentario; todo lo no exportado puede cambiar.
//
// API pública estable (SemVer del módulo):
//
//	ParseURI(raw, zona) (*URI, error)                 capsule://proyecto/servicio[?consulta]
//	Dial(ctx, uri, *Options) (net.Conn, error)        transport 1 (flujo TCP); el valor es *Conn
//	DialConn(ctx, uri, *Options) (*Conn, error)       igual, con CloseWrite, ClientAddr, Response
//	DialUDP(ctx, uri, *Options) (*PacketConn, error)  transport 2 (UDP sobre flujo, len16)
//	Listen(local, uri, *Options) (*Listener, error)   túnel local TCP o UDP hacia un servicio
//	Pipe / Stdio                                      copia bidireccional con half-close
//	EncodeRequest / DecodeRequest / EncodeResponse / DecodeResponse   códec CAP1 / CAPR
//	FrameDecoder / AppendFrame                        trama len16 de UDP
//	TokenSource, StaticToken, EnvToken                credenciales (el token nunca se imprime)
//
// Errores: GatewayError (por status/code del CAPR) con errores centinela para
// errors.Is (ErrNotAuthorized, ErrIpDenied, ErrUnavailable, ErrQuotaExceeded,
// ErrGatewayBusy, ErrTokenExpired, ErrUnsupportedVersion, ErrBadRequest,
// ErrUnsupportedTransport) y errors.As(&*GatewayError). IsRetryable(err) dice si
// el error es de los reintentables de PROTOCOL.md sección 7.
//
// Seguridad: TLS 1.3 mínimo, ALPN "capsule/1", certificado validado contra las
// raíces del sistema (o Options.RootCAs para una CA fijada); no existe opción
// para desactivar la verificación. El token solo viaja en el campo auth del
// encabezado CAP1, jamás en el URI, y ningún error, formato (%v, %+v, %#v) ni
// mensaje de la librería lo contiene.
package gatewayclient

// Version es la versión de la librería y el valor por defecto del client_id.
const Version = "0.8.0"

// ALPN es el protocolo de aplicación negociado en TLS.
const ALPN = "capsule/1"

// DefaultZone es la zona de fábrica (PROTOCOL.md sección 2.2).
const DefaultZone = "apps.tumi-ai.com"

// ZoneEnv es la variable de entorno con la zona del gateway.
const ZoneEnv = "CAPSULE_GATEWAY_ZONE"
