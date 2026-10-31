package main

import (
	"fmt"
	"strings"
)

// resolveIDPrefix lets commands accept the 8-character IDs that `list` prints.
// A full UUID is returned unchanged; a shorter prefix is matched against the
// collection at listPath and must identify exactly one item.
func resolveIDPrefix(listPath, kind, prefix string) (string, error) {
	if len(prefix) >= 36 {
		return prefix, nil
	}
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := apiClient.Get(listPath, &resp); err != nil {
		return "", err
	}
	var matches []string
	for _, it := range resp.Data {
		if strings.HasPrefix(it.ID, prefix) {
			matches = append(matches, it.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no %s with id starting %q", kind, prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("id prefix %q is ambiguous (%d %ss match); use more characters", prefix, len(matches), kind)
	}
}
