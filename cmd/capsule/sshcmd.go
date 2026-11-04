package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	gw "github.com/kynto-consulting/capsule/cli/pkg/gatewayclient"
)

// `capsule ssh`: SSH a un servicio del proyecto por el gateway usando
// `capsule connect --stdio` como ProxyCommand. El token no aparece en la línea
// de comandos de ssh ni del ProxyCommand: el proceso hijo lo lee del entorno o
// de la sesión de capsule login.

// shellQuote cita un argumento para el intérprete que usa ssh con ProxyCommand
// (cmd/CreateProcess en Windows, sh en el resto) y duplica % (ssh expande %h, %p...).
func shellQuote(s, goos string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if goos == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// proxyCommand arma "<capsule> connect --stdio [--zone z] <uri>".
func proxyCommand(exe, raw, zone, goos string) string {
	if goos == "windows" { // ssh_config trata \ como escape: barras normales
		exe = strings.ReplaceAll(exe, `\`, "/")
	}
	parts := []string{shellQuote(exe, goos), "connect", "--stdio"}
	if zone != "" {
		parts = append(parts, "--zone", shellQuote(zone, goos))
	}
	parts = append(parts, shellQuote(raw, goos))
	return strings.Join(parts, " ")
}

// splitSSHExtra separa las opciones de ssh del comando remoto: el primer argumento que no
// es opción (ni valor de una opción con argumento) empieza el comando remoto.
func splitSSHExtra(extra []string) (opts, remote []string) {
	withArg := "BEIJLOQRSWbcDeFIiJLlmOopWw"
	for i := 0; i < len(extra); i++ {
		a := extra[i]
		if a == "--" {
			return extra[:i], extra[i+1:]
		}
		if len(a) < 2 || a[0] != '-' {
			return extra[:i], extra[i:]
		}
		if len(a) == 2 && strings.ContainsRune(withArg, rune(a[1])) && i+1 < len(extra) {
			i++
		}
	}
	return extra, nil
}

func sshAlias(u *gw.URI) string { return u.Project + "-" + u.Service + ".capsule" }

// sshArgs devuelve los argumentos de ssh (sin el ejecutable).
// extra: opciones de ssh seguidas, opcionalmente, del comando remoto. Orden final:
// opciones propias, opciones del usuario, alias (host) y por último el comando remoto.
func sshArgs(u *gw.URI, proxy, user string, extra []string) []string {
	alias := sshAlias(u)
	a := []string{"-o", "ProxyCommand=" + proxy, "-o", "HostKeyAlias=" + alias}
	if user != "" {
		a = append(a, "-l", user)
	}
	opts, remote := splitSSHExtra(extra)
	a = append(a, opts...)
	a = append(a, alias)
	return append(a, remote...)
}

// sshConfigBlock es el bloque para ~/.ssh/config.
func sshConfigBlock(u *gw.URI, proxy, user string) string {
	alias := sshAlias(u)
	var b strings.Builder
	fmt.Fprintf(&b, "Host %s\n  HostName %s\n  HostKeyAlias %s\n", alias, alias, alias)
	if user != "" {
		fmt.Fprintf(&b, "  User %s\n", user)
	}
	fmt.Fprintf(&b, "  ProxyCommand %s\n", proxy)
	return b.String()
}

var sshCmd = &cobra.Command{
	Use:   "ssh <capsule://proyecto/servicio | proyecto/servicio> [-l usuario] [--print] [-- argumentos de ssh...]",
	Short: "SSH a un servicio del proyecto a través del gateway",
	Long: `Ejecuta ssh con ` + "`capsule connect --stdio`" + ` como ProxyCommand: la conexión viaja por el
gateway (TLS 1.3) sin abrir puertos ni usar la IP del servicio. El token se toma de
CAPSULE_GATEWAY_TOKEN, CAPSULE_TOKEN o de capsule login y no aparece en ninguna línea
de comandos.

  capsule ssh mi-proyecto/ssh -l deploy
  capsule ssh mi-proyecto/ssh -l deploy -- -i ~/.ssh/id_ed25519 uptime
  capsule ssh mi-proyecto/ssh --print     (muestra el bloque para ~/.ssh/config y no ejecuta ssh)

Requiere el binario nativo de capsule y un cliente ssh (OpenSSH) en el PATH. Devuelve el
código de salida de ssh.`,
	PersistentPreRunE: gatewayPreRun,
	SilenceUsage:      true,
	Args: func(cmd *cobra.Command, args []string) error {
		n := len(args)
		if d := cmd.ArgsLenAtDash(); d >= 0 {
			n = d
		}
		if n != 1 {
			return usageErr("se espera exactamente un destino: capsule://proyecto/servicio")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireNative("ssh"); err != nil {
			return err
		}
		var extra []string
		if d := cmd.ArgsLenAtDash(); d >= 0 {
			extra = args[d:]
			args = args[:d]
		}
		user, _ := cmd.Flags().GetString("user")
		zone, _ := cmd.Flags().GetString("zone")
		printOnly, _ := cmd.Flags().GetBool("print")
		raw, u, err := parseTarget(args[0], zone)
		if err != nil {
			return err
		}
		if u.Transport != "tcp" {
			return usageErr("ssh solo admite servicios TCP")
		}
		if user != "" && (strings.HasPrefix(user, "-") || strings.ContainsAny(user, " \t\r\n")) {
			return usageErr("--user inválido")
		}
		exe, err := os.Executable()
		if err != nil {
			return codedErr(exitGeneric, "no se pudo localizar el ejecutable de capsule: %v", err)
		}
		proxy := proxyCommand(exe, raw, zone, runtime.GOOS)
		if printOnly {
			fmt.Print(sshConfigBlock(u, proxy, user))
			return nil
		}
		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			return codedErr(exitGeneric, "no se encontró el cliente ssh en el PATH: instala OpenSSH")
		}
		c := exec.Command(sshBin, sshArgs(u, proxy, user, extra)...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			var xe *exec.ExitError
			if errors.As(err, &xe) {
				return &exitError{code: xe.ExitCode(), msg: "", err: err}
			}
			return codedErr(exitGeneric, "no se pudo ejecutar ssh: %v", err)
		}
		return nil
	},
}

func init() {
	sshCmd.Flags().StringP("user", "l", "", "usuario remoto de SSH")
	sshCmd.Flags().String("zone", "", "zona del gateway (por defecto "+gw.ZoneEnv+" o "+gw.DefaultZone+")")
	sshCmd.Flags().Bool("print", false, "mostrar el bloque de ~/.ssh/config y salir")
	rootCmd.AddCommand(sshCmd)
}
