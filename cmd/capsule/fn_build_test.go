package main

import (
	"archive/tar"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFnDeploySourcePacksUploadsAndFollowsTheBuild(t *testing.T) {
	s := newStub(t)
	fnBuildPoll = 0
	var mu sync.Mutex
	var files []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz, err := gzip.NewReader(r.Body)
		if err != nil || r.Method != "PUT" {
			w.WriteHeader(400)
			return
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			mu.Lock()
			files = append(files, h.Name)
			mu.Unlock()
		}
	}))
	defer up.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM public.ecr.aws/lambda/nodejs:22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "index.mjs"), []byte("export const handler = () => {}\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "big.js"), []byte("x"), 0o644)

	key := "fn-builds/" + testProj + "/33333333-3333-3333-3333-333333333333.tar.gz"
	s.on("POST", fnPath("/uploads"), 200, `{"upload_url":"`+up.URL+`/put","source_key":"`+key+`"}`)
	s.on("POST", fnPath("/functions"), 201, `{"name":"copiar","runtime":"lambda","build":{"id":"b1","status":"queued"}}`)
	s.on("GET", fnPath("/builds/b1"), 200, `{"id":"b1","status":"succeeded","image":"123.dkr.ecr.us-east-1.amazonaws.com/capsule/fn-abc@sha256:1","log":"Building\nImage ready\n"}`)

	out, err := runCmd(t, fnDeployCmd, "", []string{"copiar"}, map[string]string{"source": dir, "runtime": "lambda", "scope": "storage:biblioteca"})
	if err != nil {
		t.Fatal(err)
	}
	var deploy seenReq
	for _, r := range s.seen {
		if r.Method == "POST" && r.Path == fnPath("/functions") {
			deploy = r
		}
	}
	if deploy.Body["source_key"] != key || deploy.Body["image"] != nil || deploy.Body["runtime"] != "lambda" {
		t.Fatalf("deploy body: %v", deploy.Body)
	}
	got := strings.Join(files, ",")
	if !strings.Contains(got, "Dockerfile") || !strings.Contains(got, "index.mjs") || strings.Contains(got, "node_modules") {
		t.Fatalf("archive: %s", got)
	}
	if !strings.Contains(out, "Image ready: 123.dkr.ecr.us-east-1.amazonaws.com/capsule/fn-abc@sha256:1") {
		t.Fatalf("output: %s", out)
	}

	// a failed build is an error (exit 1)
	s.on("POST", fnPath("/functions/copiar/builds"), 202, `{"id":"b2","status":"queued"}`)
	s.on("GET", fnPath("/builds/b2"), 200, `{"id":"b2","status":"failed","error":"docker build failed"}`)
	if _, err := runCmd(t, fnBuildCmd, "", []string{"copiar"}, map[string]string{"source": dir}); err == nil || !strings.Contains(err.Error(), "docker build failed") {
		t.Fatalf("failed build: %v", err)
	}
}

func TestFnDeployNeedsExactlyOneOfImageOrSourceAndADockerfile(t *testing.T) {
	newStub(t)
	if _, err := runCmd(t, fnDeployCmd, "", []string{"x"}, nil); err == nil {
		t.Fatal("neither --image nor --source")
	}
	if _, err := runCmd(t, fnDeployCmd, "", []string{"x"}, map[string]string{"image": "i", "source": "."}); err == nil {
		t.Fatal("both --image and --source")
	}
	if _, err := runCmd(t, fnDeployCmd, "", []string{"x"}, map[string]string{"source": t.TempDir()}); err == nil || !strings.Contains(err.Error(), "Dockerfile") {
		t.Fatalf("no Dockerfile: %v", err)
	}
}
