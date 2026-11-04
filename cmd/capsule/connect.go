package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/client"
	"github.com/kynto-consulting/capsule/cli/internal/config"
	gw "github.com/kynto-consulting/capsule/cli/pkg/gatewayclient"
)

// Comandos del gateway: `capsule connect`, `capsule tunnel` y `capsule ssh`.
//
// Requieren el binario NATIVO: abren sockets TCP/TLS y lanzan procesos, algo que
// el binario WebAssembly del paquete npm no puede hacer. En WASM terminan con un
// mensaje claro; el contenedor Node puede delegar en un binario nativo si se
// define CAPSULE_NATIVE_BIN (ver npm/bin/capsule.js).
//
// Códigos de salida (capsule help exit-codes): 2 uso (URI inválido, banderas),
// 3 sin token o token expirado, 4 no autorizado o IP no permitida, 5 cuota del
// proyecto excedida, 6 plazo agotado, 1 red/TLS/protocolo/servicio no
// disponible. Con --exec o ssh, el código del comando hijo se propaga tal cual.

const (
	envGatewayToken = "CAPSULE_GATEWAY_TOKEN"
	envGatewayCA    = "CAPSULE_GATEWAY_CA_FILE"
)

func requireNative(what string) error {
	if runtime.GOOS == "js" {
		return codedErr(exitGeneric, "`capsule %s` abre conexiones de red y necesita el binario nativo de capsule; la versión WebAssembly del paquete npm no puede abrir sockets. "+
			"Instala el binario nativo (cli: `go build ./cmd/capsule`) y, si lo usas desde npm, define CAPSULE_NATIVE_BIN con su ruta.", what)
	}
	return nil
}

// gatewayPreRun carga la configuración sin preguntar nada ni escribir en stdout
// (stdout es el flujo de datos en `connect --stdio`, usado como ProxyCommand).
func gatewayPreRun(cmd *cobra.Command, args []string) error {
	c, err := config.Load()
	if err != nil {
		return codedErr(exitGeneric, "no se pudo leer la configuración: %v", err)
	}
	cfg = c
	apiURL, _ := cmd.Flags().GetString("api-url")
	if apiURL == "" {
		apiURL = cfg.APIURL
	}
	if cfg.Token != "" && !config.TokenFromEnv() {
		refreshTokenIfNeeded(apiURL)
	}
	apiClientFor(apiURL)
	return nil
}

func apiClientFor(apiURL string) { apiClient = client.New(apiURL, cfg.Token) }

// normalizeTarget acepta capsule://p/s[?...] o la forma corta proyecto/servicio.
func normalizeTarget(arg string) (string, error) {
	if len(arg) >= 10 && strings.EqualFold(arg[:10], "capsule://") {
		return arg, nil
	}
	if strings.Contains(arg, "://") {
		return "", usageErr("destino inválido: se espera capsule://proyecto/servicio o proyecto/servicio")
	}
	return "capsule://" + arg, nil
}

func parseTarget(arg, zone string) (string, *gw.URI, error) {
	raw, err := normalizeTarget(arg)
	if err != nil {
		return "", nil, err
	}
	u, err := gw.ParseURI(raw, zone)
	if err != nil {
		return "", nil, usageErr("%v", err)
	}
	return raw, u, nil
}

// gatewayRootCAs carga la CA fijada de CAPSULE_GATEWAY_CA_FILE (despliegues privados).
func gatewayRootCAs() (*x509.CertPool, error) {
	p := strings.TrimSpace(os.Getenv(envGatewayCA))
	if p == "" {
		return nil, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, codedErr(exitUsage, "no se pudo leer %s: %v", envGatewayCA, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, codedErr(exitUsage, "%s no contiene certificados PEM válidos", envGatewayCA)
	}
	return pool, nil
}

func hasGatewayPrefix(t string) bool { _, ok := gw.AuthTypeOf(t); return ok }

// gatewayTokenSource decide la credencial. Orden: CAPSULE_GATEWAY_TOKEN, CAPSULE_TOKEN y el
// token guardado por `capsule login`, si tienen prefijo cap_/cgt_/cgc_/cgs_; si el CLI solo
// tiene una sesión de usuario, pide a la API un cgc_ de un solo uso por intento. El token no
// se imprime nunca.
func gatewayTokenSource(u *gw.URI) (gw.TokenSource, error) {
	if t := strings.TrimSpace(os.Getenv(envGatewayToken)); t != "" {
		if !hasGatewayPrefix(t) {
			return nil, codedErr(exitAuth, "%s no tiene el formato de un token del gateway (cap_, cgt_, cgc_ o cgs_)", envGatewayToken)
		}
		return gw.NewStaticToken(t), nil
	}
	if t := strings.TrimSpace(os.Getenv("CAPSULE_TOKEN")); hasGatewayPrefix(t) {
		return gw.NewStaticToken(t), nil
	}
	if cfg != nil && hasGatewayPrefix(cfg.Token) {
		return gw.NewStaticToken(cfg.Token), nil
	}
	if cfg != nil && cfg.Token != "" && apiClient != nil {
		return gw.TokenFunc(func(ctx context.Context) (string, error) { return mintConnectionToken(u) }), nil
	}
	return nil, codedErr(exitAuth, "no hay credenciales para el gateway: ejecuta `capsule login` o define %s (cap_, cgt_ o cgs_)", envGatewayToken)
}

// mintConnectionToken pide un cgc_ de vida corta (POST .../connection-token).
func mintConnectionToken(u *gw.URI) (string, error) {
	return mintConnectionTokenFor(cfg.OrgID, u)
}

// mintConnectionTokenFor usa la ruta real del backend:
// POST /api/v1/orgs/{org}/projects/{proyecto}/gateway/services/{servicio}/connection-token
// (org y proyecto como UUID; el slug del proyecto se resuelve con la lista de la organización).
func mintConnectionTokenFor(orgID string, u *gw.URI) (string, error) {
	if orgID == "" {
		return "", errors.New("falta la organización: define CAPSULE_ORG_ID o org_id en ~/.capsule/config.yaml")
	}
	project := u.Project
	if !looksLikeUUID(project) {
		id, err := resolveProjectID(orgID, u.Project)
		if err != nil {
			return "", fmt.Errorf("no se pudo resolver el proyecto: %w", err)
		}
		project = id
	}
	var out struct {
		Token string `json:"token"`
	}
	path := fmt.Sprintf("/api/v1/orgs/%s/projects/%s/gateway/services/%s/connection-token", url.PathEscape(orgID), url.PathEscape(project), url.PathEscape(u.Service))
	if err := apiClient.Post(path, struct{}{}, &out); err != nil {
		return "", fmt.Errorf("no se pudo obtener un token de conexión del servidor: %w", err)
	}
	if !hasGatewayPrefix(out.Token) {
		return "", errors.New("el servidor no devolvió un token de conexión válido")
	}
	return out.Token, nil
}

// gatewayExit traduce un error de la librería a un mensaje en español y un código de salida.
func gatewayExit(err error) error {
	if err == nil {
		return nil
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return err
	}
	code, msg := exitGeneric, ""
	switch {
	case errors.Is(err, context.Canceled):
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		code, msg = exitTimeout, "se agotó el plazo de la conexión al gateway"
	case errors.Is(err, gw.ErrNoToken), errors.Is(err, gw.ErrBadToken):
		code, msg = exitAuth, "credenciales del gateway ausentes o con formato inválido: ejecuta `capsule login` o revisa "+envGatewayToken
	case errors.Is(err, gw.ErrTokenExpired):
		code, msg = exitAuth, "el token de conexión expiró: vuelve a intentarlo o renueva la sesión con `capsule login`"
	case errors.Is(err, gw.ErrNotAuthorized):
		code, msg = exitDenied, "no autorizado: el token no es válido o no tiene acceso a ese proyecto/servicio (el gateway no distingue entre ambos casos)"
	case errors.Is(err, gw.ErrIpDenied):
		code, msg = exitDenied, "tu IP de origen no está en la lista de acceso del proyecto"
	case errors.Is(err, gw.ErrQuotaExceeded):
		code, msg = exitBlocked, "cuota del proyecto excedida (conexiones o tasa); reintenta en unos segundos"
	case errors.Is(err, gw.ErrUnavailable), errors.Is(err, gw.ErrGatewayBusy):
		msg = "servicio o gateway no disponible, reintenta en un momento"
	case errors.Is(err, gw.ErrUnsupportedVersion), errors.Is(err, gw.ErrUnsupportedTransport):
		msg = "el gateway no soporta esta versión del protocolo o este transporte; actualiza capsule"
	case errors.Is(err, gw.ErrTLS):
		msg = "no se pudo establecer TLS 1.3 verificado con el gateway (certificado, nombre o ALPN)"
	case errors.Is(err, gw.ErrProtocol):
		msg = "respuesta inválida del gateway (error de protocolo capsule/1)"
	case errors.Is(err, gw.ErrUnexpectedClose):
		msg = "el gateway cerró la conexión sin cierre ordenado: los datos pueden estar truncados"
	}
	detail := err.Error()
	if msg == "" {
		msg, detail = detail, ""
	} else if detail == msg {
		detail = ""
	}
	if detail != "" {
		detail = "\n  detalle: " + detail
	}
	return &exitError{code: code, msg: "capsule: " + msg + detail, err: err}
}

type tunnelParams struct {
	target string
	zone   string
	listen string
	stdio  bool
	exec   []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	quiet  bool
	retry  *gw.RetryPolicy // nil = política por defecto (PROTOCOL.md sección 7)
}

func (p *tunnelParams) options(u *gw.URI) (*gw.Options, error) {
	ts, err := gatewayTokenSource(u)
	if err != nil {
		return nil, err
	}
	pool, err := gatewayRootCAs()
	if err != nil {
		return nil, err
	}
	o := &gw.Options{TokenSource: ts, Zone: p.zone, RootCAs: pool, ClientID: "capsule-cli/" + version, Retry: p.retry}
	if !validClientVersion(version) {
		o.ClientID = ""
	}
	o.OnError = func(err error) {
		fmt.Fprintln(p.stderr, gatewayMessage(err))
	}
	return o, nil
}

func validClientVersion(v string) bool {
	if v == "" || len(v) > 40 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+') {
			return false
		}
	}
	return true
}

func gatewayMessage(err error) string {
	if e := gatewayExit(err); e != nil {
		return strings.SplitN(e.Error(), "\n", 2)[0]
	}
	return "capsule: " + err.Error()
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// runStdio conecta stdin/stdout con el servicio (ProxyCommand de SSH).
func runStdio(ctx context.Context, p tunnelParams) error {
	_, u, err := parseTarget(p.target, p.zone)
	if err != nil {
		return err
	}
	o, err := p.options(u)
	if err != nil {
		return err
	}
	if u.Transport != "tcp" {
		return usageErr("--stdio solo admite servicios TCP")
	}
	conn, err := gw.DialURI(ctx, u, o)
	if err != nil {
		return gatewayExit(err)
	}
	if err := gw.Stdio(ctx, p.stdin, p.stdout, conn); err != nil {
		return gatewayExit(err)
	}
	return nil
}

// runTunnel abre el túnel local; con exec, lanza el comando con PGHOST/PGPORT (y
// DATABASE_URL, si existe) apuntando al puerto local y devuelve su código de salida.
func runTunnel(ctx context.Context, p tunnelParams) error {
	raw, u, err := parseTarget(p.target, p.zone)
	if err != nil {
		return err
	}
	o, err := p.options(u)
	if err != nil {
		return err
	}
	listen := p.listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	if host, _, err := net.SplitHostPort(listen); err != nil {
		return usageErr("--listen debe ser host:puerto (por ejemplo 127.0.0.1:5433)")
	} else if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
		fmt.Fprintf(p.stderr, "capsule: aviso: %s no es una dirección local; cualquiera que alcance ese puerto usará tus credenciales\n", host)
	} else if ip != nil && ip.IsUnspecified() {
		fmt.Fprintln(p.stderr, "capsule: aviso: escuchar en todas las interfaces expone el túnel (y tus credenciales) a la red")
	}
	l, err := gw.Listen(listen, raw, o)
	if err != nil {
		return codedErr(exitGeneric, "no se pudo escuchar en %s: %v", listen, err)
	}
	defer l.Close()
	if len(p.exec) == 0 {
		if !p.quiet {
			fmt.Fprintf(p.stderr, "Túnel local: %s -> capsule://%s (%s). Ctrl-C para cerrar.\n", l.Addr(), u.Route(), u.Transport)
		}
		if err := l.Serve(ctx); err != nil {
			return codedErr(exitGeneric, "el túnel terminó con error: %v", err)
		}
		return nil
	}
	go l.Serve(ctx)
	host, port, _ := net.SplitHostPort(l.Addr().String())
	if host == "::" || host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	cmd := exec.CommandContext(ctx, p.exec[0], p.exec[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.stdin, p.stdout, p.stderr
	cmd.Env = execEnv(os.Environ(), host, port)
	if err := cmd.Run(); err != nil {
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			return &exitError{code: xe.ExitCode(), msg: "", err: err}
		}
		if ctx.Err() != nil {
			return nil
		}
		return codedErr(exitGeneric, "no se pudo ejecutar %q: %v", p.exec[0], err)
	}
	return nil
}

// execEnv construye el entorno del comando hijo: PGHOST/PGPORT al túnel, DATABASE_URL
// reescrita hacia el túnel si estaba definida, y sin CAPSULE_GATEWAY_TOKEN.
func execEnv(base []string, host, port string) []string {
	out := make([]string, 0, len(base)+6)
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "PGHOST", "PGPORT", envGatewayToken:
			continue
		case "DATABASE_URL":
			if nu, ok := rewriteDatabaseURL(v, host, port); ok {
				out = append(out, k+"="+nu)
				continue
			}
		}
		out = append(out, kv)
	}
	return append(out, "PGHOST="+host, "PGPORT="+port,
		"CAPSULE_TUNNEL_HOST="+host, "CAPSULE_TUNNEL_PORT="+port, "CAPSULE_TUNNEL_ADDR="+net.JoinHostPort(host, port))
}

// rewriteDatabaseURL cambia host y puerto de una URL de base de datos conservando
// usuario, contraseña, base y parámetros.
func rewriteDatabaseURL(raw, host, port string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	u.Host = net.JoinHostPort(host, port)
	return u.String(), true
}

func connectCommand(use, short, long string, tunnelOnly bool) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Long: long, PersistentPreRunE: gatewayPreRun, SilenceUsage: true}
	c.Flags().String("listen", "", "dirección local host:puerto (por defecto 127.0.0.1:0, puerto libre)")
	c.Flags().String("zone", "", "zona del gateway (por defecto "+gw.ZoneEnv+" o "+gw.DefaultZone+")")
	if !tunnelOnly {
		c.Flags().Bool("stdio", false, "usar stdin/stdout como un único túnel (ProxyCommand de SSH)")
		c.Flags().Bool("exec", false, "ejecutar el comando que sigue a `--` con PGHOST/PGPORT/DATABASE_URL apuntando al túnel")
	}
	return c
}

var connectCmd = func() *cobra.Command {
	c := connectCommand("connect <capsule://proyecto/servicio | proyecto/servicio> [--listen host:puerto] [--stdio] [--exec -- comando...]",
		"Conecta con un servicio de un proyecto a través del gateway",
		`Abre un túnel local hacia un servicio del proyecto por el gateway de Capsule
(capsule/1: TLS 1.3, ALPN capsule/1). El token se toma de CAPSULE_GATEWAY_TOKEN,
CAPSULE_TOKEN o de la sesión de capsule login, y nunca se imprime.

  capsule connect capsule://mi-proyecto/db --listen 127.0.0.1:5433
  capsule connect mi-proyecto/db --exec -- psql -U app midb
  DATABASE_URL=postgres://u:p@h/db capsule connect mi-proyecto/db --exec -- ./migrar
  capsule connect --stdio capsule://mi-proyecto/ssh        (ProxyCommand de SSH)

Con --exec se inyectan PGHOST y PGPORT (y DATABASE_URL, si ya estaba definida, con
host y puerto del túnel) y se devuelve el código de salida del comando.
El URI acepta ?transport=udp para servicios UDP (túnel UDP local).

Requiere el binario nativo de capsule (no funciona en la versión WebAssembly de npm).
Variables: CAPSULE_GATEWAY_ZONE (zona), CAPSULE_GATEWAY_CA_FILE (CA fijada para
despliegues privados). Códigos de salida: 2 uso, 3 sin credenciales, 4 no autorizado
o IP no permitida, 5 cuota, 6 plazo, 1 red o servicio no disponible.`, false)
	c.Args = func(cmd *cobra.Command, args []string) error {
		n := len(args)
		if d := cmd.ArgsLenAtDash(); d >= 0 {
			n = d
		}
		if n != 1 {
			return usageErr("se espera exactamente un destino: capsule://proyecto/servicio")
		}
		return nil
	}
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if err := requireNative("connect"); err != nil {
			return err
		}
		dash := cmd.ArgsLenAtDash()
		var command []string
		if dash >= 0 {
			command = args[dash:]
			args = args[:dash]
		}
		stdio, _ := cmd.Flags().GetBool("stdio")
		doExec, _ := cmd.Flags().GetBool("exec")
		listen, _ := cmd.Flags().GetString("listen")
		zone, _ := cmd.Flags().GetString("zone")
		switch {
		case doExec && len(command) == 0:
			return usageErr("--exec necesita un comando después de `--`: capsule connect <destino> --exec -- psql ...")
		case !doExec && len(command) > 0:
			return usageErr("los argumentos después de `--` solo se usan con --exec")
		case stdio && (doExec || listen != ""):
			return usageErr("--stdio no se combina con --listen ni --exec")
		}
		ctx, stop := signalContext()
		defer stop()
		p := tunnelParams{target: args[0], zone: zone, listen: listen, stdio: stdio, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
		if stdio {
			p.stderr = os.Stderr // stdout queda libre para el flujo de datos
			return runStdio(ctx, p)
		}
		if doExec {
			p.exec = command
		}
		return runTunnel(ctx, p)
	}
	return c
}()

var tunnelCmd = func() *cobra.Command {
	c := connectCommand("tunnel <capsule://proyecto/servicio | proyecto/servicio> [--listen host:puerto]",
		"Túnel local hacia un servicio del proyecto (equivale a connect --listen)",
		`Reenvía una dirección local (por defecto 127.0.0.1, puerto libre) a un servicio del
proyecto a través del gateway y se queda en primer plano hasta Ctrl-C.

  capsule tunnel mi-proyecto/db --listen 127.0.0.1:5433
  psql -h 127.0.0.1 -p 5433 midb

Requiere el binario nativo de capsule. Ver también: capsule connect --help.`, true)
	c.Args = cobra.ExactArgs(1)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if err := requireNative("tunnel"); err != nil {
			return err
		}
		listen, _ := cmd.Flags().GetString("listen")
		zone, _ := cmd.Flags().GetString("zone")
		ctx, stop := signalContext()
		defer stop()
		return runTunnel(ctx, tunnelParams{target: args[0], zone: zone, listen: listen, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr})
	}
	return c
}()

func init() {
	rootCmd.AddCommand(connectCmd, tunnelCmd)
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
