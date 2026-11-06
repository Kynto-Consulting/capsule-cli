package config

import (
	"os"
	"path/filepath"
	"testing"
)

func clearHomeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA", "USER", "LOGNAME", "USERNAME"} {
		t.Setenv(k, "")
	}
}

// PowerShell and cmd define USERPROFILE but not HOME; the CLI must still find the home directory.
func TestHomeDirFallsBackToUserProfile(t *testing.T) {
	clearHomeEnv(t)
	want := filepath.Join(t.TempDir(), "user")
	t.Setenv("USERPROFILE", want)
	got, err := HomeDir()
	if err != nil || got != want {
		t.Fatalf("HomeDir() = %q, %v; want %q", got, err, want)
	}
}

func TestHomeDirFromHomeDriveAndPath(t *testing.T) {
	clearHomeEnv(t)
	t.Setenv("HOMEDRIVE", "C:")
	t.Setenv("HOMEPATH", `\Users\ana`)
	got, err := HomeDir()
	if err != nil || got != `C:\Users\ana` {
		t.Fatalf("HomeDir() = %q, %v", got, err)
	}
}

func TestHomeDirFromLocalAppData(t *testing.T) {
	clearHomeEnv(t)
	home := filepath.Join(t.TempDir(), "ana")
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	got, err := HomeDir()
	if err != nil || got != home {
		t.Fatalf("HomeDir() = %q, %v; want %q", got, err, home)
	}
}

func TestHomeDirPrefersHome(t *testing.T) {
	clearHomeEnv(t)
	want := t.TempDir()
	t.Setenv("HOME", want)
	t.Setenv("USERPROFILE", filepath.Join(want, "otra"))
	got, err := HomeDir()
	if err != nil || got == "" {
		t.Fatalf("HomeDir() = %q, %v", got, err)
	}
}

func TestConfigDirUsesHomeDir(t *testing.T) {
	clearHomeEnv(t)
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	dir, err := configDir()
	if err != nil || dir != filepath.Join(home, ".capsule") {
		t.Fatalf("configDir() = %q, %v", dir, err)
	}
}

// An empty api_url in the file must fall back to the default instead of producing "Invalid URL".
func TestLoadEmptyAPIURLUsesDefault(t *testing.T) {
	clearHomeEnv(t)
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".capsule")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("api_url: \"\"\ntoken: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil || c.APIURL != defaultAPIURL {
		t.Fatalf("Load() APIURL = %q, %v", c.APIURL, err)
	}
}
