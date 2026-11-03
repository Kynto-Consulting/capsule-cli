package gatewayclient

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errores centinela para errors.Is.
var (
	ErrBadRequest           = errors.New("solicitud rechazada por el gateway")
	ErrUnsupportedVersion   = errors.New("versión del protocolo no soportada por el gateway")
	ErrUnsupportedTransport = errors.New("transporte no soportado o no habilitado en el gateway")
	ErrNotAuthorized        = errors.New("no autorizado")
	ErrTokenExpired         = errors.New("token de conexión expirado")
	ErrIpDenied             = errors.New("IP de origen no permitida por el proyecto")
	ErrUnavailable          = errors.New("servicio no disponible")
	ErrQuotaExceeded        = errors.New("cuota excedida")
	ErrGatewayBusy          = errors.New("gateway ocupado")
	ErrUnknownStatus        = errors.New("estado desconocido del gateway")

	ErrProtocol        = errors.New("error de protocolo capsule/1")
	ErrTLS             = errors.New("fallo de TLS con el gateway")
	ErrNoToken         = errors.New("no hay token de acceso al gateway")
	ErrBadToken        = errors.New("formato de token inválido")
	ErrUnexpectedClose = errors.New("el gateway cerró la conexión sin close_notify (posible truncamiento)")
	ErrWrongTransport  = errors.New("el transporte del URI no corresponde a esta función")
	ErrDatagramTooBig  = errors.New("datagrama demasiado grande")
	ErrEmptyDatagram   = errors.New("datagrama vacío no representable (len=0 es keepalive)")
	ErrClosed          = errors.New("conexión cerrada")
	ErrIncomplete      = errors.New("faltan bytes")
	ErrInvalidField    = errors.New("campo inválido")
)

// GatewayError es la respuesta CAPR con status distinto de 0.
type GatewayError struct {
	Status     uint8
	Code       uint16
	Message    string        // msg del CAPR (ASCII; sin el token)
	Version    uint8         // CAPR.version
	MaxVersion uint8         // TLV max_version (0 si no vino)
	RetryAfter time.Duration // TLV retry_after (0 si no vino)
	kind       error
}

func (e *GatewayError) Error() string {
	m := e.Message
	if m != "" {
		m = ": " + m
	}
	return fmt.Sprintf("gateway: %v (estado %d, código 0x%04x)%s", e.kind, e.Status, e.Code, m)
}

// Unwrap expone el error centinela de la clase.
func (e *GatewayError) Unwrap() error { return e.kind }

// Retryable indica si PROTOCOL.md sección 7 permite reintentar automáticamente.
func (e *GatewayError) Retryable() bool {
	switch e.Status {
	case 1:
		return e.Code == 0x010A
	case 4, 5, 6:
		return true
	}
	return false
}

// newGatewayError clasifica (status, code) según la tabla de la sección 7.
func newGatewayError(r *Response, redact []string) *GatewayError {
	e := &GatewayError{Status: r.Status, Code: r.Code, Version: r.Version, Message: redactText(r.Msg, redact)}
	if r.TLVs.MaxVersion != nil {
		e.MaxVersion = *r.TLVs.MaxVersion
	}
	if r.TLVs.RetryAfter != nil {
		e.RetryAfter = time.Duration(*r.TLVs.RetryAfter) * time.Second
	}
	switch r.Status {
	case 1:
		switch r.Code {
		case 0x0101:
			e.kind = ErrUnsupportedVersion
		case 0x0109:
			e.kind = ErrUnsupportedTransport
		default:
			e.kind = ErrBadRequest
		}
	case 2:
		if r.Code == 0x0202 {
			e.kind = ErrTokenExpired
		} else {
			e.kind = ErrNotAuthorized
		}
	case 3:
		e.kind = ErrIpDenied
	case 4:
		e.kind = ErrUnavailable
	case 5:
		e.kind = ErrQuotaExceeded
	case 6:
		e.kind = ErrGatewayBusy
	default:
		e.kind = ErrUnknownStatus
	}
	return e
}

// IsRetryable dice si err es de los que la librería reintenta sola (o que el
// llamador puede reintentar con retroceso): estados 4, 5, 6, 0x010A y los
// fallos de red sin respuesta. Los errores de TLS, de token, de URI y los
// estados 2 y 3 no lo son.
func IsRetryable(err error) bool {
	var ge *GatewayError
	if errors.As(err, &ge) {
		return ge.Retryable()
	}
	var de *DialError
	if errors.As(err, &de) {
		return de.retryable
	}
	return false
}

// DialError es un fallo antes de obtener la respuesta del gateway (red, TLS,
// ausencia o truncamiento de CAPR).
type DialError struct {
	Phase     string // "red", "tls", "respuesta"
	Err       error
	kind      error
	retryable bool
}

func (e *DialError) Error() string { return fmt.Sprintf("%v (%s): %v", e.kind, e.Phase, e.Err) }
func (e *DialError) Unwrap() []error {
	return []error{e.kind, e.Err}
}

// ProtocolError es una violación del formato CAP1/CAPR (nombres de los vectores).
type ProtocolError struct {
	Kind   string // truncated, bad_magic, bad_version, msg_too_long, bad_msg, ext_too_long, bad_ext, frame_too_large
	Detail string
}

func (e *ProtocolError) Error() string {
	if e.Detail == "" {
		return "capsule/1: " + e.Kind
	}
	return "capsule/1: " + e.Kind + " (" + e.Detail + ")"
}
func (e *ProtocolError) Is(t error) bool { return t == ErrProtocol }

// RequestError es el error que el decodificador de solicitudes (lado gateway)
// asigna: status y code de PROTOCOL.md sección 5.
type RequestError struct {
	Status uint8
	Code   uint16
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("encabezado CAP1 inválido (estado %d, código 0x%04x)", e.Status, e.Code)
}

// URIError es un fallo del analizador de URI; Kind usa los nombres normativos
// de la sección 2.3. Nunca incluye el URI ni valores de parámetros.
type URIError struct {
	Kind   string
	Detail string
}

func (e *URIError) Error() string {
	if e.Detail == "" {
		return "URI capsule:// inválido (" + e.Kind + ")"
	}
	return "URI capsule:// inválido (" + e.Kind + "): " + e.Detail
}

func redactText(s string, secrets []string) string {
	for _, t := range secrets {
		if t != "" {
			s = strings.ReplaceAll(s, t, "[token]")
		}
	}
	return s
}
