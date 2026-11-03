package gatewayclient

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// TokenSource entrega la credencial para cada intento de conexión. Un cgc_ es
// de un solo uso: una fuente que lo emite debe devolver uno nuevo en cada
// llamada.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// RenewableTokenSource además sabe renovar un token expirado (estado 0x0202).
// Dial llama a Renew como máximo una vez por Dial.
type RenewableTokenSource interface {
	TokenSource
	Renew(ctx context.Context) (string, error)
}

// TokenFunc adapta una función a TokenSource.
type TokenFunc func(ctx context.Context) (string, error)

func (f TokenFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// StaticToken es una fuente de un token fijo. Su representación textual
// nunca contiene el token.
type StaticToken struct{ token string }

// NewStaticToken crea la fuente.
func NewStaticToken(token string) StaticToken { return StaticToken{token: token} }

func (s StaticToken) Token(context.Context) (string, error) {
	if s.token == "" {
		return "", ErrNoToken
	}
	return s.token, nil
}
func (s StaticToken) String() string               { return "StaticToken([redactado])" }
func (s StaticToken) GoString() string             { return s.String() }
func (s StaticToken) Format(f fmt.State, _ rune)   { fmt.Fprint(f, s.String()) }
func (s StaticToken) MarshalText() ([]byte, error) { return []byte("[redactado]"), nil }
func (s StaticToken) MarshalJSON() ([]byte, error) { return []byte(`"[redactado]"`), nil }

// EnvToken lee el token de las variables de entorno indicadas, en orden
// (por defecto CAPSULE_GATEWAY_TOKEN y CAPSULE_TOKEN), cada vez que se pide.
type EnvToken struct{ Vars []string }

func (e EnvToken) Token(context.Context) (string, error) {
	vars := e.Vars
	if len(vars) == 0 {
		vars = []string{"CAPSULE_GATEWAY_TOKEN", "CAPSULE_TOKEN"}
	}
	for _, v := range vars {
		if t := strings.TrimSpace(os.Getenv(v)); t != "" {
			if _, ok := AuthTypeOf(t); ok {
				return t, nil
			}
		}
	}
	return "", ErrNoToken
}
