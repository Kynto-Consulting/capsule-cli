package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func parseWorkflow(t *testing.T, y string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(y), &m); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, y)
	}
	jobs, _ := m["jobs"].(map[string]interface{})
	for _, j := range []string{"ci", "preview", "deploy"} {
		if jobs[j] == nil {
			t.Fatalf("missing job %s", j)
		}
	}
	return m
}

func TestWorkflowNodePnpm(t *testing.T) {
	d := filepath.ToSlash(t.TempDir())
	write(t, d, "package.json", `{"scripts":{"lint":"x","test":"y","build":"z"}}`)
	write(t, d, "pnpm-lock.yaml", "lockfileVersion: 9")
	st := detectCIStack(d, "", "auto")
	if st.Name != "node-pnpm" {
		t.Fatalf("stack %s", st.Name)
	}
	y := generateWorkflow(st, ciOptions{Branch: "main", ActionRef: "main", WorkflowName: "Capsule CI/CD"})
	parseWorkflow(t, y)
	for _, want := range []string{"pnpm install --frozen-lockfile", "pnpm run --if-present lint", "pnpm run --if-present build", "secrets.CAPSULE_TOKEN"} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(y, "typecheck") {
		t.Error("must not invent missing scripts")
	}
}

func TestWorkflowGoMonorepo(t *testing.T) {
	d := filepath.ToSlash(t.TempDir())
	write(t, d, "svc/go.mod", "module x\n")
	y := generateWorkflow(detectCIStack(d+"/svc", "svc", "auto"), ciOptions{Branch: "main", Dir: "svc", ActionRef: "main", WorkflowName: "n"})
	parseWorkflow(t, y)
	for _, want := range []string{"go-version-file: svc/go.mod", "working-directory: svc", "go test ./..."} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestWorkflowPythonAndOther(t *testing.T) {
	d := filepath.ToSlash(t.TempDir())
	write(t, d, "requirements.txt", "flask\npytest\n")
	parseWorkflow(t, generateWorkflow(detectCIStack(d, "", "auto"), ciOptions{Branch: "main", ActionRef: "main", WorkflowName: "n"}))
	e := filepath.ToSlash(t.TempDir())
	st := detectCIStack(e, "", "auto")
	if st.Name != "other" {
		t.Fatalf("got %s", st.Name)
	}
	parseWorkflow(t, generateWorkflow(st, ciOptions{Branch: "main", ActionRef: "main", WorkflowName: "n"}))
}
