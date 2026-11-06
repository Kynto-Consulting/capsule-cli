package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

func TestParseSizeAndHuman(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "unlimited": 0, "500": 500, "1kb": 1024, "10GB": 10 << 30, "1.5GiB": 3 << 29, "2T": 2 << 40, "5 mb": 5 << 20} {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1GB", "1.2.3GB"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) accepted", bad)
		}
	}
	if humanBytes(1536) != "1.5 KiB" || humanBytes(10) != "10 B" || limitStr(0) != "unlimited" {
		t.Fatalf("human: %s %s %s", humanBytes(1536), humanBytes(10), limitStr(0))
	}
}

func TestSplitObjectRefAndPick(t *testing.T) {
	for in, want := range map[string][2]string{
		"files/a/b.txt":                      {"files", "a/b.txt"},
		"s3://files/a.txt":                   {"files", "a.txt"},
		"capsule://shop/files/dir/x":         {"files", "dir/x"},
		"capsule://shop/files":               {"files", ""},
		"files":                              {"files", ""},
		"shop-acme-files/key with space.txt": {"shop-acme-files", "key with space.txt"},
	} {
		if b, k := splitObjectRef(in); b != want[0] || k != want[1] {
			t.Errorf("splitObjectRef(%q) = %q,%q", in, b, k)
		}
	}
	items := []storageItem{{ID: "aaaa1111", Name: "files", Bucket: "shop-acme-files", Engine: "objstore"}, {ID: "bbbb2222", Name: "old", DBName: "capsule-acme-old", Engine: "s3"}}
	for ref, id := range map[string]string{"files": "aaaa1111", "shop-acme-files": "aaaa1111", "old": "bbbb2222", "capsule-acme-old": "bbbb2222", "bbbb": "bbbb2222"} {
		b, err := pickStorage(items, ref)
		if err != nil || b.ID != id {
			t.Errorf("pickStorage(%q) = %v, %v", ref, b, err)
		}
	}
	if _, err := pickStorage(items, "nope"); err == nil {
		t.Error("unknown bucket accepted")
	}
}

func TestExpiresSeconds(t *testing.T) {
	c := &cobra.Command{}
	c.Flags().String("expires", "", "")
	for in, want := range map[string]int{"": 3600, "90": 90, "15m": 900, "2h": 7200, "7d": 604800} {
		_ = c.Flags().Set("expires", in)
		if got, err := expiresSeconds(c); err != nil || got != want {
			t.Errorf("%q -> %d, %v", in, got, err)
		}
	}
	_ = c.Flags().Set("expires", "soon")
	if _, err := expiresSeconds(c); err == nil {
		t.Error("garbage accepted")
	}
}

// fakeStorage is a minimal Capsule API + S3 data plane for the transfer tests.
type fakeStorage struct {
	mu        sync.Mutex
	srv       *httptest.Server
	parts     map[int][]byte
	partPuts  map[int]int
	failPart  int // part number to fail once
	failed    bool
	completed []byte
	single    map[string][]byte
	uploadID  string
	presigned []string
}

func newFakeStorage(t *testing.T) *fakeStorage {
	f := &fakeStorage{parts: map[int][]byte{}, partPuts: map[int]int{}, single: map[string][]byte{}, uploadID: "0123456789abcdef0123456789abcdef"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	apiClient = client.New(f.srv.URL, "t")
	return f
}

var testTime = time.Unix(1700000000, 0)

const apiRoot = "/api/v1/orgs/o1/projects/p1/storage"

func (f *fakeStorage) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == apiRoot && r.Method == "GET":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "b1", "name": "files", "bucket": "shop-acme-files", "engine": "objstore"}}})
	case r.URL.Path == apiRoot+"/b1/presign":
		var req struct {
			Key, Method string
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.presigned = append(f.presigned, req.Method+" "+req.Key)
		_ = json.NewEncoder(w).Encode(map[string]string{"url": f.srv.URL + "/data/" + req.Key})
	case r.URL.Path == apiRoot+"/b1/uploads" && r.Method == "POST":
		var req struct {
			Size     int64  `json:"size"`
			PartSize int64  `json:"part_size"`
			UploadID string `json:"upload_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := int((req.Size + req.PartSize - 1) / req.PartSize)
		var parts []map[string]any
		for i := 1; i <= n; i++ {
			sz := req.PartSize
			if i == n {
				sz = req.Size - int64(n-1)*req.PartSize
			}
			parts = append(parts, map[string]any{"number": i, "size": sz, "url": fmt.Sprintf("%s/part/%d", f.srv.URL, i)})
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": f.uploadID, "part_size": req.PartSize, "parts": parts})
	case r.URL.Path == apiRoot+"/b1/uploads/"+f.uploadID && r.Method == "GET":
		var ps []map[string]any
		for n, b := range f.parts {
			ps = append(ps, map[string]any{"number": n, "size": len(b), "etag": fmt.Sprintf("etag%d", n)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"parts": ps})
	case r.URL.Path == apiRoot+"/b1/uploads/"+f.uploadID+"/complete":
		var req struct {
			Parts []struct {
				Number int
				ETag   string
			}
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var all []byte
		for _, p := range req.Parts {
			if p.ETag != fmt.Sprintf("etag%d", p.Number) {
				http.Error(w, `{"error":{"code":"INVALID_PART","message":"bad etag"}}`, 400)
				return
			}
			all = append(all, f.parts[p.Number]...)
		}
		f.completed = all
		_ = json.NewEncoder(w).Encode(map[string]any{"key": "big.bin", "size": len(all)})
	case strings.HasPrefix(r.URL.Path, "/part/") && r.Method == "PUT":
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/part/"))
		if n == f.failPart && !f.failed {
			f.failed = true
			http.Error(w, "boom", 500)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.parts[n] = b
		f.partPuts[n]++
		w.Header().Set("ETag", fmt.Sprintf(`"etag%d"`, n))
	case strings.HasPrefix(r.URL.Path, "/data/") && r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		f.single[strings.TrimPrefix(r.URL.Path, "/data/")] = b
	case strings.HasPrefix(r.URL.Path, "/data/") && r.Method == "GET":
		b, ok := f.single[strings.TrimPrefix(r.URL.Path, "/data/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "", testTime, bytes.NewReader(b))
	default:
		http.NotFound(w, r)
	}
}

func TestPutSingleAndGetRoundTrip(t *testing.T) {
	f := newFakeStorage(t)
	b := &storageItem{ID: "b1", Name: "files", Engine: "objstore"}
	dir := t.TempDir()
	src := filepath.Join(dir, "hello.txt")
	_ = os.WriteFile(src, []byte("hello capsule"), 0o644)
	if err := uploadFile("o1", "p1", b, src, "docs/hello.txt", "", 0); err != nil {
		t.Fatal(err)
	}
	if string(f.single["docs/hello.txt"]) != "hello capsule" || f.presigned[0] != "PUT docs/hello.txt" {
		t.Fatalf("single put: %q %v", f.single, f.presigned)
	}
	u, _ := presignURL("o1", "p1", "b1", "docs/hello.txt", "get", 60)
	dest := filepath.Join(dir, "out", "copy.txt")
	n, err := downloadResumable(u, dest)
	if err != nil || n != 13 {
		t.Fatalf("download: %d %v", n, err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "hello capsule" {
		t.Fatalf("content %q", got)
	}
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal(".part left behind")
	}
}

func TestMultipartResumeSkipsStoredParts(t *testing.T) {
	f := newFakeStorage(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LocalAppData", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	data := make([]byte, 18<<20)
	_, _ = rand.Read(data)
	src := filepath.Join(t.TempDir(), "big.bin")
	_ = os.WriteFile(src, data, 0o644)
	st, _ := os.Stat(src)
	b := &storageItem{ID: "b1", Name: "files", Engine: "objstore"}

	// a previous run stored parts 1 and 2 (5 MiB each) and was interrupted
	const ps = 5 << 20
	f.parts[1], f.parts[2] = data[:ps], data[ps:2*ps]
	sf := stateFile("o1", "p1", "b1", "big.bin", src, st.Size(), st.ModTime().UnixNano())
	saveState(sf, &uploadState{UploadID: f.uploadID, Key: "big.bin", Size: st.Size(), ModTime: st.ModTime().UnixNano(), PartSize: ps,
		ETags: map[int]string{1: "etag1", 2: "etag2"}})

	if err := uploadFile("o1", "p1", b, src, "big.bin", "", ps); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.completed, data) {
		t.Fatalf("assembled object differs (%d vs %d bytes)", len(f.completed), len(data))
	}
	if f.partPuts[1] != 0 || f.partPuts[2] != 0 || f.partPuts[3] != 1 || f.partPuts[4] != 1 {
		t.Fatalf("parts re-sent on resume: %v", f.partPuts)
	}
	if _, err := os.Stat(sf); err == nil {
		t.Fatal("state file kept after completion")
	}
}

func TestMultipartRetriesTransientPartFailure(t *testing.T) {
	f := newFakeStorage(t)
	f.failPart = 2
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LocalAppData", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	data := make([]byte, 17<<20)
	_, _ = rand.Read(data)
	src := filepath.Join(t.TempDir(), "x.bin")
	_ = os.WriteFile(src, data, 0o644)
	if err := uploadFile("o1", "p1", &storageItem{ID: "b1", Name: "files"}, src, "big.bin", "", 5<<20); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.completed, data) || f.partPuts[2] != 1 {
		t.Fatalf("transient failure not retried: %v", f.partPuts)
	}
}

func TestDownloadResumesWithRange(t *testing.T) {
	f := newFakeStorage(t)
	body := bytes.Repeat([]byte("0123456789"), 1000)
	f.single["big.dat"] = body
	u, _ := presignURL("o1", "p1", "b1", "big.dat", "get", 60)
	dest := filepath.Join(t.TempDir(), "big.dat")
	// a previous attempt stored the first 4000 bytes and the object's ETag
	_ = os.WriteFile(dest+".part", body[:4000], 0o644)
	_ = os.WriteFile(dest+".part.etag", []byte(`"v1"`), 0o644)
	n, err := downloadResumable(u, dest)
	if err != nil || n != int64(len(body)) {
		t.Fatalf("resume: %d %v", n, err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, body) {
		t.Fatal("resumed file differs")
	}
	// a changed object (different ETag) is downloaded from scratch
	body2 := bytes.Repeat([]byte("abcdefghij"), 500)
	f.single["big.dat"] = body2
	_ = os.WriteFile(dest+".part", body[:4000], 0o644)
	_ = os.WriteFile(dest+".part.etag", []byte(`"stale"`), 0o644)
	if _, err := downloadResumable(u, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, body2) {
		t.Fatal("stale partial data was kept")
	}
}

func TestS3ErrorText(t *testing.T) {
	got := s3ErrorText([]byte(`<?xml version="1.0"?><Error><Code>QuotaExceeded</Code><Message>full</Message></Error>`))
	if got != "QuotaExceeded: full" {
		t.Fatal(got)
	}
}
