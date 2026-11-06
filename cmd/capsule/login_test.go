package main

import (
	"testing"

	"github.com/kynto-consulting/capsule/cli/internal/config"
)

func TestPersistAPIURLKeepsLoginAddress(t *testing.T) {
	c := &config.Config{APIURL: ""}
	persistAPIURL(c, " https://api.tumi-ai.com/ ")
	if c.APIURL != "https://api.tumi-ai.com" {
		t.Fatalf("APIURL = %q", c.APIURL)
	}
	persistAPIURL(c, "")
	if c.APIURL != "https://api.tumi-ai.com" {
		t.Fatalf("an empty flag must keep the saved address, got %q", c.APIURL)
	}
}
