package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/spf13/pflag"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

const aiOrg, aiProj = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"

type aiServer struct {
	mu     sync.Mutex
	srv    *httptest.Server
	hits   []string
	forms  map[string]map[string]string
	files  map[string]string // field -> content, last request
	polls  int
	failAs string // when set, every AI call answers this error code
}

func newAIServer(t *testing.T) *aiServer {
	s := &aiServer{forms: map[string]map[string]string{}, files: map[string]string{}}
	base := "/api/v1/orgs/" + aiOrg + "/projects/" + aiProj + "/ai"
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.hits = append(s.hits, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		if s.failAs != "" && strings.HasPrefix(r.URL.Path, base) {
			code := map[string]int{"QUOTA_EXCEEDED": 429, "MODEL_UNAVAILABLE": 409, "MODEL_ACCESS": 403}[s.failAs]
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":{"code":"` + s.failAs + `","message":"nope"}}`))
			return
		}
		form := map[string]string{}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(8 << 20); err == nil {
				for k, v := range r.MultipartForm.Value {
					form[k] = v[0]
				}
				for k, fh := range r.MultipartForm.File {
					f, _ := fh[0].Open()
					b, _ := io.ReadAll(f)
					s.files[k] = string(b)
				}
			}
		} else if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			for k, v := range m {
				b, _ := json.Marshal(v)
				form[k] = strings.Trim(string(b), `"`)
			}
		}
		s.forms[r.Method+" "+r.URL.Path] = form
		switch {
		case r.URL.Path == "/api/v1/ai/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"transcribe","name":"Amazon Transcribe","provider":"Amazon","capabilities":["stt"],"available":true,"price":{"unit":"minute","per":0.024}},
				{"id":"voxtral","name":"V","provider":"Mistral","capabilities":["chat"],"available":false,"reason":"access not granted"}],
				"capabilities":[{"id":"image-generate","available":false,"reason":"no model"},{"id":"stt","available":true,"models":1}]}`))
		case r.URL.Path == base+"/transcribe":
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"job":{"id":"job-1","status":"queued"}}`))
		case r.URL.Path == base+"/jobs/job-1":
			if r.URL.Query().Get("format") != "" {
				w.Header().Set("Content-Type", "application/x-subrip")
				_, _ = w.Write([]byte("1\n00:00:00,000 --> 00:00:01,000\nHola\n\n"))
				return
			}
			s.polls++
			st := "running"
			if s.polls >= 2 {
				st = "completed"
			}
			_, _ = w.Write([]byte(`{"job":{"id":"job-1","status":"` + st + `","cost_estimate_usd":0.01,"result":{"text":"Hola"}}}`))
		case r.URL.Path == base+"/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Header().Set("X-Capsule-Cost-USD", "0.0006")
			_, _ = w.Write([]byte("MP3DATA"))
		case r.URL.Path == base+"/images":
			png := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
			_, _ = w.Write([]byte(`{"model":"stability-image-control-sketch","images":[{"format":"png","b64":"` + png + `"},{"format":"png","b64":"` + png + `"}],"cost_estimate_usd":0.14}`))
		case r.URL.Path == base+"/embeddings":
			_, _ = w.Write([]byte(`{"model":"titan-embed-v2","dimensions":3,"vectors":[[1,2,3]],"usage":{"tokens":4},"cost_estimate_usd":0.000001}`))
		case r.URL.Path == base+"/embeddings/search":
			_, _ = w.Write([]byte(`{"documents":2,"hits":[{"key":"a.txt","score":0.91,"snippet":"factura de luz"}]}`))
		case r.URL.Path == base+"/rerank":
			_, _ = w.Write([]byte(`{"results":[{"index":1,"score":0.9,"document":"dos"}],"cost_estimate_usd":0.001}`))
		case r.URL.Path == base+"/chat":
			if form["stream"] == "true" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"delta\":\"es una \"}\n\ndata: {\"delta\":\"manzana\"}\n\ndata: {\"done\":true,\"cost_estimate_usd\":0.002}\n\n"))
				return
			}
			_, _ = w.Write([]byte(`{"text":"es una manzana","model":"nova-2-lite","cost_estimate_usd":0.002}`))
		case r.URL.Path == base+"/talk":
			wav := base64.StdEncoding.EncodeToString([]byte("RIFFWAV"))
			_, _ = w.Write([]byte(`{"model":"nova-2-sonic","heard":"hello","answer":"hi there","audio_base64":"` + wav + `","audio_seconds":1.5,"cost_estimate_usd":0.01}`))
		case r.URL.Path == base+"/usage":
			_, _ = w.Write([]byte(`{"used_usd":0.07,"limit_usd":5,"unlimited":false,"day":"2026-11-05"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"` + r.URL.Path + `"}}`))
		}
	}))
	t.Cleanup(s.srv.Close)
	old := apiClient
	apiClient = client.New(s.srv.URL, "tok")
	t.Cleanup(func() { apiClient = old })
	t.Setenv("CAPSULE_ORG_ID", aiOrg)
	t.Setenv("CAPSULE_PROJECT_ID", aiProj)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	return s
}

// runAI runs `capsule ai ...` the way cobra does after the persistent pre-run (which would read the user's config): it finds the
// subcommand, parses its flags and calls RunE with a fresh flag set.
func runAI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd, rest, err := aiCmd.Find(args)
	if err != nil {
		return "", err
	}
	defer cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	})
	if err = cmd.ParseFlags(rest); err != nil {
		return "", err
	}
	if cmd.Args != nil {
		if err = cmd.Args(cmd, cmd.Flags().Args()); err != nil {
			return "", err
		}
	}
	out := capture(t, func() { err = cmd.RunE(cmd, cmd.Flags().Args()) })
	return out, err
}

func TestAIModelsAndCapabilities(t *testing.T) {
	newAIServer(t)
	out, err := runAI(t, "models", "--capability", "stt")
	if err != nil || !strings.Contains(out, "transcribe") || !strings.Contains(out, "unavailable: access not granted") || !strings.Contains(out, "$0.024/min") {
		t.Fatalf("models: %v\n%s", err, out)
	}
	out, _ = runAI(t, "models", "--json")
	var j struct {
		Models       []map[string]any `json:"models"`
		Capabilities []map[string]any `json:"capabilities"`
	}
	if json.Unmarshal([]byte(out), &j) != nil || len(j.Models) != 2 || len(j.Capabilities) != 2 {
		t.Fatalf("models --json: %s", out)
	}
	if got := capAliases("image"); len(got) != 2 || got[0] != "image-generate" {
		t.Errorf("image alias: %v", got)
	}
	if got := capAliases("speech"); got[0] != "tts" {
		t.Errorf("speech alias: %v", got)
	}
}

func TestAITranscribeWaitsAndWritesSRT(t *testing.T) {
	s := newAIServer(t)
	dir := t.TempDir()
	audio := filepath.Join(dir, "meeting.mp3")
	_ = os.WriteFile(audio, []byte("AUDIOBYTES"), 0o644)
	srt := filepath.Join(dir, "meeting.srt")
	// speed up the poll loop is not needed: the second poll answers completed after one 3 s sleep
	_, err := runAI(t, "transcribe", audio, "--diarize", "--speakers", "2", "--lang", "es-US", "-o", srt)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(srt)
	if !strings.Contains(string(b), "00:00:00,000 --> 00:00:01,000\nHola") {
		t.Fatalf("srt: %q", b)
	}
	f := s.forms["POST /api/v1/orgs/"+aiOrg+"/projects/"+aiProj+"/ai/transcribe"]
	if f["diarize"] != "true" || f["speakers"] != "2" || f["lang"] != "es-US" || s.files["file"] != "AUDIOBYTES" {
		t.Fatalf("form sent: %v files=%v", f, s.files)
	}
	if _, err = runAI(t, "transcribe", filepath.Join(dir, "missing.mp3")); exitCodeFor(err) != exitUsage {
		t.Errorf("a missing file is a usage error: %v", err)
	}
	if _, err = runAI(t, "transcribe"); exitCodeFor(err) != exitUsage {
		t.Errorf("no file: %v", err)
	}
}

func TestAISpeakImageEmbedRerankUsage(t *testing.T) {
	s := newAIServer(t)
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "hola.mp3")
	if _, err := runAI(t, "speak", "Hola", "mundo", "--voice", "Lupe", "-o", mp3); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(mp3); string(b) != "MP3DATA" {
		t.Fatalf("audio file: %q", b)
	}
	if f := s.forms["POST /api/v1/orgs/"+aiOrg+"/projects/"+aiProj+"/ai/speech"]; f["text"] != "Hola mundo" || f["voice"] != "Lupe" {
		t.Fatalf("speech body: %v", f)
	}
	if _, err := runAI(t, "speak", "hola"); exitCodeFor(err) != exitUsage {
		t.Errorf("speak without -o: %v", err)
	}

	imgs := filepath.Join(dir, "out")
	out, err := runAI(t, "image", "una manzana", "--n", "2", "--size", "512x512", "-o", imgs)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"image-1.png", "image-2.png"} {
		if b, _ := os.ReadFile(filepath.Join(imgs, n)); string(b) != "PNGDATA" {
			t.Fatalf("%s: %q (stdout %s)", n, b, out)
		}
	}
	if f := s.forms["POST /api/v1/orgs/"+aiOrg+"/projects/"+aiProj+"/ai/images"]; f["prompt"] != "una manzana" || f["n"] != "2" || f["size"] != "512x512" {
		t.Fatalf("image form: %v", f)
	}
	src := filepath.Join(dir, "in.png")
	_ = os.WriteFile(src, []byte("SRC"), 0o644)
	if _, err = runAI(t, "image", "--image", src, "--operation", "remove-background", "-o", filepath.Join(dir, "cut")); err != nil || s.files["image"] != "SRC" {
		t.Fatalf("edit: %v files=%v", err, s.files)
	}
	if f := s.forms["POST /api/v1/orgs/"+aiOrg+"/projects/"+aiProj+"/ai/images"]; f["operation"] != "remove-background" {
		t.Fatalf("operation: %v", f)
	}

	out, err = runAI(t, "embed", "hola", "--json")
	if err != nil || !strings.Contains(out, `"dimensions":3`) {
		t.Fatalf("embed: %v %s", err, out)
	}
	out, err = runAI(t, "embed", "search", "recibo luz", "--bucket", "docs", "--top", "3")
	if err != nil || !strings.Contains(out, "0.910  docs/a.txt") {
		t.Fatalf("search: %v %s", err, out)
	}
	if _, err = runAI(t, "embed", "search", "x"); exitCodeFor(err) != exitUsage {
		t.Errorf("search without bucket: %v", err)
	}
	out, err = runAI(t, "rerank", "capital", "--doc", "uno", "--doc", "dos")
	if err != nil || !strings.Contains(out, "0.900  [1] dos") {
		t.Fatalf("rerank: %v %s", err, out)
	}
	out, err = runAI(t, "usage")
	if err != nil || !strings.Contains(out, "$0.0700 of $5.00") {
		t.Fatalf("usage: %v %s", err, out)
	}
}

func TestAIChatAttachmentsAndStream(t *testing.T) {
	s := newAIServer(t)
	img := filepath.Join(t.TempDir(), "p.png")
	_ = os.WriteFile(img, []byte("IMG"), 0o644)
	out, err := runAI(t, "chat", "qué fruta es", "--image", img)
	if err != nil || !strings.Contains(out, "es una manzana") || s.files["image"] != "IMG" {
		t.Fatalf("chat: %v %s files=%v", err, out, s.files)
	}
	out, err = runAI(t, "chat", "qué fruta es", "--image", img, "--stream")
	if err != nil || !strings.Contains(out, "es una manzana") {
		t.Fatalf("stream: %v %q", err, out)
	}
	if _, err = runAI(t, "chat", "--image", img); exitCodeFor(err) != exitUsage {
		t.Errorf("attachment without question: %v", err)
	}
}

func TestAITalkFile(t *testing.T) {
	s := newAIServer(t)
	dir := t.TempDir()
	in, out := filepath.Join(dir, "q.wav"), filepath.Join(dir, "a.wav")
	_ = os.WriteFile(in, []byte("WAVIN"), 0o644)
	text, err := runAI(t, "talk", in, "-o", out, "--voice", "tiffany")
	if err != nil || !strings.Contains(text, "heard:    hello") || !strings.Contains(text, "answered: hi there") {
		t.Fatalf("talk: %v %s", err, text)
	}
	if b, _ := os.ReadFile(out); string(b) != "RIFFWAV" || s.files["audio"] != "WAVIN" {
		t.Fatalf("answer file %q sent %v", b, s.files)
	}
}

func TestAIErrorsMapToExitCodes(t *testing.T) {
	s := newAIServer(t)
	for code, want := range map[string]int{"QUOTA_EXCEEDED": exitBlocked, "MODEL_UNAVAILABLE": exitBlocked, "MODEL_ACCESS": exitBlocked} {
		s.failAs = code
		_, err := runAI(t, "usage")
		if exitCodeFor(err) != want || !strings.Contains(err.Error(), code) {
			t.Errorf("%s: exit %d err %v", code, exitCodeFor(err), err)
		}
	}
	s.failAs = "QUOTA_EXCEEDED"
	if _, err := runAI(t, "usage"); !strings.Contains(err.Error(), "capsule ai usage") {
		t.Errorf("the quota error says what to do next: %v", err)
	}
	s.failAs = "MODEL_UNAVAILABLE"
	if _, err := runAI(t, "usage"); !strings.Contains(err.Error(), "capsule ai models --available") {
		t.Errorf("the model error says what to do next: %v", err)
	}
}

func TestAITalkLive(t *testing.T) {
	var got []byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/talk/ticket"):
			_, _ = w.Write([]byte(`{"ticket":"tk1","ws":"/api/v1/ai/talk/ws?ticket=tk1"}`))
		case r.URL.Path == "/api/v1/ai/talk/ws":
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			for {
				typ, data, err := c.Read(r.Context())
				if err != nil {
					return
				}
				if typ == websocket.MessageText && string(data) == "end" {
					_ = c.Close(websocket.StatusNormalClosure, "bye")
					return
				}
				mu.Lock()
				if len(got) == 0 { // answer as soon as the speech starts arriving
					_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"text","role":"assistant","text":"hi"}`))
					_ = c.Write(r.Context(), websocket.MessageBinary, []byte("PCM24"))
				}
				got = append(got, data...)
				mu.Unlock()
			}
		}
	}))
	defer srv.Close()
	old := apiClient
	apiClient = client.New(srv.URL, "tok")
	defer func() { apiClient = old }()
	t.Setenv("CAPSULE_ORG_ID", aiOrg)
	t.Setenv("CAPSULE_PROJECT_ID", aiProj)

	// stdin: audio; stdout: the answer's audio
	inR, inW, _ := os.Pipe()
	go func() { _, _ = inW.Write(bytes.Repeat([]byte{1}, 5000)); inW.Close() }()
	oldIn := os.Stdin
	os.Stdin = inR
	defer func() { os.Stdin = oldIn }()
	out, err := runAI(t, "talk", "--live")
	if err != nil || !strings.Contains(out, "PCM24") {
		t.Fatalf("live: %v %q", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 5000 {
		t.Fatalf("the server got %d bytes of audio, want 5000", len(got))
	}
}
