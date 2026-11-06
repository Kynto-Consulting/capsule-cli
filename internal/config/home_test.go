package config

import (
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
