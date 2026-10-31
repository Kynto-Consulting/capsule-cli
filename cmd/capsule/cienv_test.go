package main

import "testing"

func TestParseKeyVal(t *testing.T) {
	k, v, err := parseKeyVal("A=b=c")
	if err != nil || k != "A" || v != "b=c" {
		t.Fatalf("got %q %q %v", k, v, err)
	}
	if _, _, err := parseKeyVal("noequals"); err == nil {
		t.Fatal("expected error")
	}
	if _, _, err := parseKeyVal("1BAD=x"); err == nil {
		t.Fatal("expected error for bad key")
	}
}

func TestParseEnvFile(t *testing.T) {
	m, err := parseEnvFile("# c\nexport A=1\nB=\"x y\"\nC='z'\nD=v # trailing\n\nE=\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "x y", "C": "z", "D": "v", "E": ""}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s=%q want %q", k, m[k], v)
		}
	}
}

func TestCollectDeployEnvMultiline(t *testing.T) {
	m, err := collectDeployEnv("", []string{"A=1\n\nB=2", "A=3"})
	if err != nil || m["A"] != "3" || m["B"] != "2" {
		t.Fatalf("%v %v", m, err)
	}
}
