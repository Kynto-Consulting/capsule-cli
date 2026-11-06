package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	"github.com/kynto-consulting/capsule/cli/internal/client"
)

// Multimodal AI (docs/AI_MULTIMODAL.md): speech to text, text to speech, speech to speech, images, embeddings, rerank and chat with
// attachments, all through the project routes /api/v1/orgs/{org}/projects/{project}/ai/.... The original `capsule ai [prompt]`
// assistant, explain-failure, dockerfile, optimize-costs and keys keep working unchanged.

func aiBase(cmd *cobra.Command) (string, error) {
	org, project, err := resolveOrgProject(cmd)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/v1/orgs/%s/projects/%s/ai", org, project), nil
}

// addAIScope gives a subcommand the flags every AI call needs.
func addAIScope(c *cobra.Command) {
	c.Flags().String("org", "", "Org ID (default: linked directory)")
	c.Flags().String("project", "", "Project ID (default: linked directory)")
	c.Flags().Bool("json", false, "Print the full response as JSON")
	c.Flags().String("model", "", "Model id (see: capsule ai models)")
}

func aiWantJSON(cmd *cobra.Command) bool {
	if j, _ := cmd.Flags().GetBool("json"); j {
		return true
	}
	o, _ := cmd.Flags().GetString("output")
	return o == "json"
}

// aiError adds the next step to the errors users hit most.
func aiError(err error) error {
	ae, ok := client.AsAPIError(err)
	if !ok {
		return err
	}
	msg := fmt.Sprintf("error %s: %s", orStr(ae.Code, fmt.Sprintf("HTTP_%d", ae.Status)), ae.Message)
	switch ae.Code {
	case "MODEL_UNAVAILABLE", "NO_MODEL":
		msg += "\n  List what works with: capsule ai models --available"
	case "MODEL_ACCESS":
		msg += "\n  The model needs access in the AWS account (an administrator enables it in Bedrock > Model access)."
	case "QUOTA_EXCEEDED":
		msg += "\n  The project's daily AI budget is spent; see capsule ai usage."
	case "BUCKET_NOT_FOUND":
		msg += "\n  Buckets of the project: capsule storage list"
	}
	if ae.Status == 401 {
		msg += "\n  Not signed in or the token is invalid: run `capsule login` (or set CAPSULE_TOKEN)."
	}
	if ae.RequestID != "" {
		msg += "\n  request_id: " + ae.RequestID
	}
	return &exitError{code: classifyAPI(ae), msg: msg, err: err}
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- multipart bodies ----

type aiField struct{ name, value string }
type aiFile struct{ field, path string }

// postForm sends fields and files as multipart and returns the response (caller closes the body).
func postForm(path string, fields []aiField, files []aiFile, timeout time.Duration) (*http.Response, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var err error
		defer func() { _ = pw.CloseWithError(err) }()
		for _, f := range fields {
			if f.value == "" {
				continue
			}
			if err = mw.WriteField(f.name, f.value); err != nil {
				return
			}
		}
		for _, f := range files {
			var fh *os.File
			if fh, err = os.Open(f.path); err != nil {
				return
			}
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, f.field, filepath.Base(f.path)))
			if ct := mime.TypeByExtension(filepath.Ext(f.path)); ct != "" {
				h.Set("Content-Type", ct)
			}
			var w io.Writer
			if w, err = mw.CreatePart(h); err == nil {
				_, err = io.Copy(w, fh)
			}
			fh.Close()
			if err != nil {
				return
			}
		}
		err = mw.Close()
	}()
	return apiClient.Raw("POST", path, pr, map[string]string{"Content-Type": mw.FormDataContentType()}, timeout)
}

func postJSON(path string, body any, timeout time.Duration, accept string) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	h := map[string]string{"Content-Type": "application/json"}
	if accept != "" {
		h["Accept"] = accept
	}
	return apiClient.Raw("POST", path, bytes.NewReader(b), h, timeout)
}

func decodeJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func checkFile(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return usageErr("%v", err)
	}
	if st.IsDir() {
		return usageErr("%s is a directory", p)
	}
	return nil
}

func printCost(cost float64) {
	if cost > 0 {
		fmt.Fprintf(os.Stderr, "estimated cost: $%.4f\n", cost)
	}
}

// ---- models ----

type aiModel struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	Service      string   `json:"service"`
	Capabilities []string `json:"capabilities"`
	Available    bool     `json:"available"`
	Reason       string   `json:"reason"`
	Verified     bool     `json:"verified"`
	Note         string   `json:"note"`
	Price        struct {
		Unit   string  `json:"unit"`
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
		Per    float64 `json:"per"`
	} `json:"price"`
}

type aiCapInfo struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Models    int    `json:"models"`
	Reason    string `json:"reason"`
}

// capAliases lets the commands and flags use the short names people think in.
func capAliases(c string) []string {
	switch strings.ToLower(c) {
	case "":
		return []string{""}
	case "image", "images":
		return []string{"image-generate", "image-edit"}
	case "speech", "voice", "tts":
		return []string{"tts"}
	case "transcribe", "stt":
		return []string{"stt"}
	case "s2s", "talk", "speech-to-speech":
		return []string{"speech-to-speech"}
	case "embeddings", "embed":
		return []string{"embed"}
	}
	return []string{strings.ToLower(c)}
}

func priceText(m aiModel) string {
	p := m.Price
	switch p.Unit {
	case "1k_tokens":
		if p.Input == 0 && p.Output == 0 {
			return "tokens (price n/a)"
		}
		if p.Output == 0 {
			return fmt.Sprintf("$%g/1k tok", p.Input)
		}
		return fmt.Sprintf("$%g in, $%g out /1k tok", p.Input, p.Output)
	case "image":
		return fmt.Sprintf("$%g/image", p.Per)
	case "minute":
		return fmt.Sprintf("$%g/min", p.Per)
	case "1m_chars":
		return fmt.Sprintf("$%g/1M chars", p.Per)
	case "1k_searches":
		return fmt.Sprintf("$%g/1k searches", p.Per)
	}
	return ""
}

var aiModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List the AI models by capability, with access and price",
	Long: `The catalog is built from Bedrock (plus Amazon Transcribe and Polly) and grouped by capability:
chat, vision, stt, tts, speech-to-speech, image-generate, image-edit, embed, rerank, video.
Models the account cannot use are listed with the reason. Short names work: image, speech, transcribe, embeddings.`,
	Example: `  capsule ai models
  capsule ai models --capability stt
  capsule ai models --capability image --available --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		capFlag, _ := cmd.Flags().GetString("capability")
		onlyAvail, _ := cmd.Flags().GetBool("available")
		var models []aiModel
		var caps []aiCapInfo
		seen := map[string]bool{}
		for _, c := range capAliases(capFlag) {
			q := url.Values{}
			if c == "" {
				q.Set("catalog", "1")
			} else {
				q.Set("capability", c)
			}
			if onlyAvail {
				q.Set("available", "true")
			}
			var resp struct {
				Data         []aiModel   `json:"data"`
				Capabilities []aiCapInfo `json:"capabilities"`
			}
			if err := apiClient.Get("/api/v1/ai/models?"+q.Encode(), &resp); err != nil {
				return aiError(err)
			}
			caps = resp.Capabilities
			for _, m := range resp.Data {
				if !seen[m.ID] {
					seen[m.ID] = true
					models = append(models, m)
				}
			}
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"models": models, "capabilities": caps})
		}
		sort.SliceStable(models, func(i, j int) bool { return models[i].Provider < models[j].Provider })
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "MODEL\tCAPABILITIES\tSTATUS\tPRICE")
		for _, m := range models {
			st := "ok"
			if !m.Available {
				st = "unavailable: " + m.Reason
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.ID, strings.Join(m.Capabilities, ","), st, priceText(m))
		}
		_ = w.Flush()
		var empty []string
		for _, c := range caps {
			if !c.Available && (capFlag == "" || containsStr(capAliases(capFlag), c.ID)) {
				empty = append(empty, fmt.Sprintf("  %s: %s", c.ID, c.Reason))
			}
		}
		if len(empty) > 0 {
			fmt.Fprintln(os.Stderr, "\nCapabilities with no usable model:\n"+strings.Join(empty, "\n"))
		}
		return nil
	},
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---- chat with attachments ----

var aiChatCmd = &cobra.Command{
	Use:   "chat [prompt]",
	Short: "Ask a model, with image, video, document or audio attachments",
	Long: `Without a prompt or attachments it opens the interactive assistant (same as: capsule ai).
With a prompt it asks the project's chat models; attach files with --image, --video, --document and --audio
(audio is transcribed first and the transcript goes with the question).`,
	Example: `  capsule ai chat "what is in this picture?" --image photo.jpg
  capsule ai chat "summarize" --document report.pdf --stream
  capsule ai chat "what was decided?" --audio meeting.mp3 --lang es-US`,
	RunE: func(cmd *cobra.Command, args []string) error {
		imgs, _ := cmd.Flags().GetStringArray("image")
		vids, _ := cmd.Flags().GetStringArray("video")
		docs, _ := cmd.Flags().GetStringArray("document")
		auds, _ := cmd.Flags().GetStringArray("audio")
		if len(args) == 0 && len(imgs)+len(vids)+len(docs)+len(auds) == 0 {
			return aiCmd.RunE(aiCmd, nil)
		}
		if len(args) == 0 {
			return usageErr("write the question as an argument: capsule ai chat \"...\" --image file")
		}
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		model, _ := cmd.Flags().GetString("model")
		system, _ := cmd.Flags().GetString("system")
		lang, _ := cmd.Flags().GetString("lang")
		stream, _ := cmd.Flags().GetBool("stream")
		maxTok, _ := cmd.Flags().GetInt("max-tokens")
		fields := []aiField{{"prompt", strings.Join(args, " ")}, {"model", model}, {"system", system}, {"lang", lang}}
		if maxTok > 0 {
			fields = append(fields, aiField{"max_tokens", fmt.Sprint(maxTok)})
		}
		if stream && !aiWantJSON(cmd) {
			fields = append(fields, aiField{"stream", "true"})
		}
		var files []aiFile
		for field, list := range map[string][]string{"image": imgs, "video": vids, "document": docs, "audio": auds} {
			for _, p := range list {
				if err := checkFile(p); err != nil {
					return err
				}
				files = append(files, aiFile{field, p})
			}
		}
		resp, err := postForm(base+"/chat", fields, files, 5*time.Minute)
		if err != nil {
			return aiError(err)
		}
		if stream && !aiWantJSON(cmd) {
			return readSSE(resp.Body)
		}
		var out struct {
			Text  string  `json:"text"`
			Model string  `json:"model"`
			Cost  float64 `json:"cost_estimate_usd"`
		}
		if err := decodeJSON(resp, &out); err != nil {
			return err
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(out)
		}
		fmt.Println(out.Text)
		printCost(out.Cost)
		return nil
	},
}

// readSSE prints the deltas of a streamed chat answer as they arrive.
func readSSE(body io.ReadCloser) error {
	defer body.Close()
	dec := json.NewDecoder(body)
	_ = dec
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 2048)
	var cost float64
	handle := func(line string) error {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" {
			return nil
		}
		var ev struct {
			Delta string  `json:"delta"`
			Done  bool    `json:"done"`
			Err   string  `json:"error"`
			Cost  float64 `json:"cost_estimate_usd"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			return nil
		}
		switch {
		case ev.Err != "":
			return fmt.Errorf("the model failed: %s", ev.Err)
		case ev.Done:
			cost = ev.Cost
		default:
			fmt.Print(ev.Delta)
		}
		return nil
	}
	for {
		n, err := body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		for {
			i := bytes.Index(buf, []byte("\n\n"))
			if i < 0 {
				break
			}
			if herr := handle(string(buf[:i])); herr != nil {
				return herr
			}
			buf = buf[i+2:]
		}
		if err != nil {
			break
		}
	}
	fmt.Println()
	printCost(cost)
	return nil
}

// ---- speech to text ----

type aiJob struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	Error    string  `json:"error"`
	Language string  `json:"language"`
	Cost     float64 `json:"cost_estimate_usd"`
	Saved    string  `json:"saved_to"`
	Result   *struct {
		Text     string  `json:"text"`
		Duration float64 `json:"duration_seconds"`
		Speakers int     `json:"speakers"`
	} `json:"result"`
}

var aiTranscribeCmd = &cobra.Command{
	Use:   "transcribe FILE",
	Short: "Transcribe audio to text (Amazon Transcribe), with speakers and timestamps",
	Long: `Uploads the audio, waits for the job and writes the transcript. -o picks the format by extension:
.txt, .srt, .vtt or .json (json keeps every segment with start, end and speaker). Without -o the text goes to stdout.
Use --source bucket/key instead of FILE to read the audio from a bucket of the project, and --save-to bucket/key to store the result there.`,
	Example: `  capsule ai transcribe meeting.mp3 --diarize --lang es-US -o meeting.srt
  capsule ai transcribe --source media/interview.m4a --save-to media/transcripts/ --no-wait`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		source, _ := cmd.Flags().GetString("source")
		if len(args) == 0 && source == "" {
			return usageErr("give an audio file, or --source bucket/key")
		}
		model, _ := cmd.Flags().GetString("model")
		lang, _ := cmd.Flags().GetString("lang")
		diarize, _ := cmd.Flags().GetBool("diarize")
		speakers, _ := cmd.Flags().GetInt("speakers")
		saveTo, _ := cmd.Flags().GetString("save-to")
		noWait, _ := cmd.Flags().GetBool("no-wait")
		out, _ := cmd.Flags().GetString("out")
		fields := []aiField{{"model", model}, {"lang", lang}, {"save_to", saveTo}, {"file_source", source}}
		if diarize {
			fields = append(fields, aiField{"diarize", "true"})
		}
		if speakers > 0 {
			fields = append(fields, aiField{"speakers", fmt.Sprint(speakers)})
		}
		if saveTo != "" {
			fields = append(fields, aiField{"save_format", orStr(strings.TrimPrefix(filepath.Ext(out), "."), "json")})
		}
		var files []aiFile
		if len(args) == 1 {
			if err := checkFile(args[0]); err != nil {
				return err
			}
			files = append(files, aiFile{"file", args[0]})
		}
		resp, err := postForm(base+"/transcribe", fields, files, 10*time.Minute)
		if err != nil {
			return aiError(err)
		}
		var started struct {
			Job aiJob `json:"job"`
		}
		if err := decodeJSON(resp, &started); err != nil {
			return err
		}
		if noWait {
			if aiWantJSON(cmd) {
				return json.NewEncoder(os.Stdout).Encode(started)
			}
			fmt.Printf("job %s started (%s). Fetch it with: capsule ai jobs %s -o transcript.srt\n", started.Job.ID, started.Job.Status, started.Job.ID)
			return nil
		}
		return waitJob(cmd, base, started.Job.ID, out)
	},
}

var aiJobsCmd = &cobra.Command{
	Use:   "jobs JOB-ID",
	Short: "State of a transcription job; writes the transcript when it is done",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		out, _ := cmd.Flags().GetString("out")
		wait, _ := cmd.Flags().GetBool("wait")
		if wait {
			return waitJob(cmd, base, args[0], out)
		}
		return showJob(cmd, base, args[0], out)
	},
}

func fetchJob(base, id string) (*aiJob, error) {
	var r struct {
		Job aiJob `json:"job"`
	}
	if err := apiClient.Get(base+"/jobs/"+id, &r); err != nil {
		return nil, aiError(err)
	}
	return &r.Job, nil
}

func waitJob(cmd *cobra.Command, base, id, out string) error {
	deadline := time.Now().Add(2 * time.Hour)
	last := ""
	for {
		j, err := fetchJob(base, id)
		if err != nil {
			return err
		}
		if j.Status != last {
			fmt.Fprintf(os.Stderr, "job %s: %s\n", j.ID, j.Status)
			last = j.Status
		}
		switch j.Status {
		case "completed":
			return showJob(cmd, base, id, out)
		case "failed":
			return codedErr(exitGeneric, "transcription failed: %s", j.Error)
		}
		if time.Now().After(deadline) {
			return codedErr(exitTimeout, "gave up waiting; the job keeps running: capsule ai jobs %s --wait", id)
		}
		time.Sleep(3 * time.Second)
	}
}

func showJob(cmd *cobra.Command, base, id, out string) error {
	j, err := fetchJob(base, id)
	if err != nil {
		return err
	}
	if aiWantJSON(cmd) && out == "" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"job": j})
	}
	if j.Status != "completed" {
		fmt.Printf("job %s: %s %s\n", j.ID, j.Status, j.Error)
		return nil
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(out)), ".")
	if format == "" {
		format = "txt"
	}
	resp, err := apiClient.Raw("GET", base+"/jobs/"+id+"?format="+url.QueryEscape(format), nil, nil, time.Minute)
	if err != nil {
		return aiError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out == "" {
		os.Stdout.Write(data)
	} else if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	} else {
		fmt.Fprintf(os.Stderr, "wrote %s\n", out)
	}
	if j.Saved != "" {
		fmt.Fprintf(os.Stderr, "saved to bucket: %s\n", j.Saved)
	}
	printCost(j.Cost)
	return nil
}

// ---- text to speech ----

var aiSpeakCmd = &cobra.Command{
	Use:   "speak TEXT",
	Short: "Turn text into speech (Amazon Polly)",
	Example: `  capsule ai speak "Hola, bienvenido" --voice Lupe -o hola.mp3
  capsule ai speak "Hello" --lang en-US --model polly-generative -o hello.mp3`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		out, _ := cmd.Flags().GetString("out")
		saveTo, _ := cmd.Flags().GetString("save-to")
		if out == "" && saveTo == "" {
			return usageErr("say where the audio goes: -o file.mp3 (or --save-to bucket/key)")
		}
		body := map[string]any{}
		for _, k := range []string{"model", "voice", "lang", "format", "rate"} {
			if v, _ := cmd.Flags().GetString(k); v != "" {
				body[k] = v
			}
		}
		if body["format"] == nil && out != "" {
			switch strings.ToLower(filepath.Ext(out)) {
			case ".ogg":
				body["format"] = "ogg"
			case ".pcm":
				body["format"] = "pcm"
			}
		}
		body["text"] = strings.Join(args, " ")
		if saveTo != "" {
			body["save_to"] = saveTo
			if out == "" {
				body["inline"] = "false"
			}
		}
		resp, err := postJSON(base+"/speech", body, 3*time.Minute, "")
		if err != nil {
			return aiError(err)
		}
		defer resp.Body.Close()
		cost := 0.0
		_, _ = fmt.Sscan(resp.Header.Get("X-Capsule-Cost-USD"), &cost)
		if saveTo != "" {
			var r struct {
				Saved  string  `json:"saved_to"`
				B64    string  `json:"audio_base64"`
				Cost   float64 `json:"cost_estimate_usd"`
				Voice  string  `json:"voice"`
				Format string  `json:"format"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "saved to bucket: %s\n", r.Saved)
			if out != "" && r.B64 != "" {
				b, _ := base64.StdEncoding.DecodeString(r.B64)
				if err := os.WriteFile(out, b, 0o644); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "wrote %s\n", out)
			}
			printCost(r.Cost)
			return nil
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"file": out, "bytes": len(data), "cost_estimate_usd": cost})
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", out, len(data))
		printCost(cost)
		return nil
	},
}

var aiVoicesCmd = &cobra.Command{
	Use:   "voices",
	Short: "List the speech voices",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		lang, _ := cmd.Flags().GetString("lang")
		var r struct {
			Data []struct {
				ID, Name, Lang, Gender string
				Engines                []string
			} `json:"data"`
		}
		if err := apiClient.Get(base+"/voices?lang="+url.QueryEscape(lang), &r); err != nil {
			return aiError(err)
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(r.Data)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "VOICE\tLANGUAGE\tGENDER\tENGINES")
		for _, v := range r.Data {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.ID, v.Lang, v.Gender, strings.Join(v.Engines, ","))
		}
		return w.Flush()
	},
}

// ---- images ----

var aiImageCmd = &cobra.Command{
	Use:   "image PROMPT",
	Short: "Generate or edit images",
	Long: `With no input image it generates from the prompt. With --image it edits: pick the service with --operation
(inpaint needs --mask; erase-object, remove-background, search-replace, search-recolor, outpaint, style-transfer,
style-guide, fast-upscale, conservative-upscale, creative-upscale) or --model. Results are written to the -o folder.`,
	Example: `  capsule ai image "a red apple on a wooden table" --n 2 --size 1024x1024 -o ./out/
  capsule ai image "a green circle" --image photo.png --mask mask.png --operation inpaint -o ./edited/
  capsule ai image --image photo.png --operation remove-background -o ./cutout/`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		outDir, _ := cmd.Flags().GetString("out")
		saveTo, _ := cmd.Flags().GetString("save-to")
		if outDir == "" && saveTo == "" {
			return usageErr("say where the images go: -o folder/ (or --save-to bucket/prefix/)")
		}
		fields := []aiField{{"prompt", strings.Join(args, " ")}}
		for _, k := range []string{"model", "size", "format", "operation", "save_to", "search_prompt", "select_prompt", "negative_prompt", "aspect_ratio", "seed", "control_strength", "fidelity"} {
			if v, _ := cmd.Flags().GetString(strings.ReplaceAll(k, "_", "-")); v != "" {
				fields = append(fields, aiField{k, v})
			}
		}
		if n, _ := cmd.Flags().GetInt("n"); n > 0 {
			fields = append(fields, aiField{"n", fmt.Sprint(n)})
		}
		var files []aiFile
		for _, f := range []string{"image", "mask", "style-image"} {
			if p, _ := cmd.Flags().GetString(f); p != "" {
				if err := checkFile(p); err != nil {
					return err
				}
				files = append(files, aiFile{strings.ReplaceAll(f, "-", "_"), p})
			}
		}
		if src, _ := cmd.Flags().GetString("image-source"); src != "" {
			fields = append(fields, aiField{"image_source", src})
		}
		if len(args) == 0 && len(files) == 0 {
			return usageErr("give a prompt and/or --image")
		}
		if outDir == "" {
			fields = append(fields, aiField{"inline", "false"})
		}
		resp, err := postForm(base+"/images", fields, files, 5*time.Minute)
		if err != nil {
			return aiError(err)
		}
		var r struct {
			Model  string `json:"model"`
			Images []struct {
				Format string `json:"format"`
				Seed   int64  `json:"seed"`
				B64    string `json:"b64"`
				Saved  string `json:"saved_to"`
			} `json:"images"`
			Cost float64 `json:"cost_estimate_usd"`
		}
		if err := decodeJSON(resp, &r); err != nil {
			return err
		}
		var written []string
		if outDir != "" {
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
		}
		for i, im := range r.Images {
			if im.Saved != "" {
				fmt.Fprintf(os.Stderr, "saved to bucket: %s\n", im.Saved)
			}
			if outDir == "" || im.B64 == "" {
				continue
			}
			b, err := base64.StdEncoding.DecodeString(im.B64)
			if err != nil {
				return err
			}
			p := filepath.Join(outDir, fmt.Sprintf("image-%d.%s", i+1, im.Format))
			if err := os.WriteFile(p, b, 0o644); err != nil {
				return err
			}
			written = append(written, p)
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"model": r.Model, "files": written, "cost_estimate_usd": r.Cost})
		}
		for _, p := range written {
			fmt.Println(p)
		}
		fmt.Fprintf(os.Stderr, "model: %s\n", r.Model)
		printCost(r.Cost)
		return nil
	},
}

// ---- embeddings and rerank ----

var aiEmbedCmd = &cobra.Command{
	Use:   "embed [TEXT...]",
	Short: "Embeddings: vectors for text or images; `embed search` finds similar files in a bucket",
	Example: `  capsule ai embed "hello" "hola" --json
  capsule ai embed --image photo.png --model nova-embed
  capsule ai embed search "electricity invoice" --bucket docs --top 3`,
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		model, _ := cmd.Flags().GetString("model")
		dim, _ := cmd.Flags().GetInt("dimensions")
		imgs, _ := cmd.Flags().GetStringArray("image")
		if len(args) == 0 && len(imgs) == 0 {
			return usageErr("give text or --image")
		}
		fields := []aiField{{"model", model}}
		if dim > 0 {
			fields = append(fields, aiField{"dimensions", fmt.Sprint(dim)})
		}
		for _, t := range args {
			fields = append(fields, aiField{"input", t})
		}
		var files []aiFile
		for _, p := range imgs {
			if err := checkFile(p); err != nil {
				return err
			}
			files = append(files, aiFile{"image", p})
		}
		resp, err := postForm(base+"/embeddings", fields, files, 2*time.Minute)
		if err != nil {
			return aiError(err)
		}
		var r struct {
			Model      string      `json:"model"`
			Dimensions int         `json:"dimensions"`
			Vectors    [][]float64 `json:"vectors"`
			Usage      struct {
				Tokens int `json:"tokens"`
			} `json:"usage"`
			Cost float64 `json:"cost_estimate_usd"`
		}
		if err := decodeJSON(resp, &r); err != nil {
			return err
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(r)
		}
		fmt.Printf("%d vector(s) of %d dimensions with %s (%d tokens). Use --json to get the numbers.\n", len(r.Vectors), r.Dimensions, r.Model, r.Usage.Tokens)
		printCost(r.Cost)
		return nil
	},
}

var aiEmbedSearchCmd = &cobra.Command{
	Use:   "search QUERY",
	Short: "Find the text files of a bucket most similar to QUERY",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		bucket, _ := cmd.Flags().GetString("bucket")
		if bucket == "" {
			return usageErr("--bucket is required (see: capsule storage list)")
		}
		prefix, _ := cmd.Flags().GetString("prefix")
		top, _ := cmd.Flags().GetInt("top")
		model, _ := cmd.Flags().GetString("model")
		resp, err := postJSON(base+"/embeddings/search", map[string]any{"query": strings.Join(args, " "), "bucket": bucket, "prefix": prefix, "top_k": top, "model": model}, 3*time.Minute, "")
		if err != nil {
			return aiError(err)
		}
		var r struct {
			Documents int `json:"documents"`
			Hits      []struct {
				Key     string  `json:"key"`
				Score   float64 `json:"score"`
				Snippet string  `json:"snippet"`
			} `json:"hits"`
			Cost float64 `json:"cost_estimate_usd"`
		}
		if err := decodeJSON(resp, &r); err != nil {
			return err
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(r)
		}
		for _, h := range r.Hits {
			fmt.Printf("%.3f  %s/%s\n       %s\n", h.Score, bucket, h.Key, h.Snippet)
		}
		fmt.Fprintf(os.Stderr, "%d documents compared\n", r.Documents)
		printCost(r.Cost)
		return nil
	},
}

var aiRerankCmd = &cobra.Command{
	Use:   "rerank QUERY",
	Short: "Order documents by relevance to a query",
	Example: `  capsule ai rerank "capital of France" --doc "Paris is in France" --doc "cats sleep"
  capsule ai rerank "refund policy" --file candidates.txt --top 3`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		docs, _ := cmd.Flags().GetStringArray("doc")
		if f, _ := cmd.Flags().GetString("file"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return usageErr("%v", err)
			}
			for _, l := range strings.Split(string(b), "\n") {
				if strings.TrimSpace(l) != "" {
					docs = append(docs, strings.TrimSpace(l))
				}
			}
		}
		if len(docs) == 0 {
			return usageErr("give documents with --doc (repeatable) or --file (one per line)")
		}
		top, _ := cmd.Flags().GetInt("top")
		model, _ := cmd.Flags().GetString("model")
		resp, err := postJSON(base+"/rerank", map[string]any{"query": strings.Join(args, " "), "documents": docs, "top_n": top, "model": model}, 2*time.Minute, "")
		if err != nil {
			return aiError(err)
		}
		var r struct {
			Results []struct {
				Index    int     `json:"index"`
				Score    float64 `json:"score"`
				Document string  `json:"document"`
			} `json:"results"`
			Cost float64 `json:"cost_estimate_usd"`
		}
		if err := decodeJSON(resp, &r); err != nil {
			return err
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(r)
		}
		for _, x := range r.Results {
			fmt.Printf("%.3f  [%d] %s\n", x.Score, x.Index, x.Document)
		}
		printCost(r.Cost)
		return nil
	},
}

// ---- speech to speech ----

var aiTalkCmd = &cobra.Command{
	Use:   "talk [FILE.wav]",
	Short: "Speech to speech (Nova Sonic): talk to the model with a recording, or live from a pipe",
	Long: `File mode: sends one recorded utterance (WAV PCM 16-bit mono) and writes the spoken answer (WAV, 24 kHz) with -o.
It prints what the model heard and what it answered.

Live mode (--live): a real conversation over WebSocket. Raw audio goes through the pipes, so any recorder and player work:
  PCM 16-bit mono 16 kHz on stdin  ->  capsule ai talk --live  ->  PCM 16-bit mono 24 kHz on stdout
Transcripts are printed on stderr. Example with ffmpeg and ffplay (use your microphone device name):
  ffmpeg -f dshow -i audio="Microphone" -f s16le -ar 16000 -ac 1 - | capsule ai talk --live | ffplay -f s16le -ar 24000 -nodelay -i -
The panel offers the same conversation with the browser microphone.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		if live, _ := cmd.Flags().GetBool("live"); live {
			return talkLive(cmd, base)
		}
		if len(args) == 0 {
			return usageErr("give a recording (WAV), or use --live with audio on stdin")
		}
		if err := checkFile(args[0]); err != nil {
			return err
		}
		out, _ := cmd.Flags().GetString("out")
		voice, _ := cmd.Flags().GetString("voice")
		system, _ := cmd.Flags().GetString("system")
		model, _ := cmd.Flags().GetString("model")
		resp, err := postForm(base+"/talk", []aiField{{"model", model}, {"voice", voice}, {"system", system}}, []aiFile{{"audio", args[0]}}, 3*time.Minute)
		if err != nil {
			return aiError(err)
		}
		var r struct {
			Heard   string  `json:"heard"`
			Answer  string  `json:"answer"`
			B64     string  `json:"audio_base64"`
			Seconds float64 `json:"audio_seconds"`
			Cost    float64 `json:"cost_estimate_usd"`
			Model   string  `json:"model"`
		}
		if err := decodeJSON(resp, &r); err != nil {
			return err
		}
		if out != "" {
			b, _ := base64.StdEncoding.DecodeString(r.B64)
			if err := os.WriteFile(out, b, 0o644); err != nil {
				return err
			}
		}
		if aiWantJSON(cmd) {
			r.B64 = ""
			return json.NewEncoder(os.Stdout).Encode(r)
		}
		fmt.Printf("heard:    %s\nanswered: %s\n", r.Heard, r.Answer)
		if out != "" {
			fmt.Fprintf(os.Stderr, "wrote %s (%.1f s of speech)\n", out, r.Seconds)
		}
		printCost(r.Cost)
		return nil
	},
}

func talkLive(cmd *cobra.Command, base string) error {
	voice, _ := cmd.Flags().GetString("voice")
	system, _ := cmd.Flags().GetString("system")
	model, _ := cmd.Flags().GetString("model")
	resp, err := postJSON(base+"/talk/ticket", map[string]string{"model": model, "voice": voice, "system": system}, time.Minute, "")
	if err != nil {
		return aiError(err)
	}
	var tk struct {
		Ticket string `json:"ticket"`
		WS     string `json:"ws"`
	}
	if err := decodeJSON(resp, &tk); err != nil {
		return err
	}
	u, err := url.Parse(apiClient.BaseURL() + tk.WS)
	if err != nil {
		return err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("opening the live connection: %w", err)
	}
	defer conn.CloseNow()
	fmt.Fprintln(os.Stderr, "live: connected; speak, and end the input (Ctrl-D or close the pipe) to finish")
	done := make(chan error, 1)
	var lastEvent atomic.Int64
	var answered atomic.Bool
	lastEvent.Store(time.Now().UnixNano())
	go func() { // server -> stdout / stderr
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				done <- nil
				return
			}
			lastEvent.Store(time.Now().UnixNano())
			if typ == websocket.MessageBinary {
				answered.Store(true)
			}
			if typ == websocket.MessageBinary {
				_, _ = os.Stdout.Write(data)
				continue
			}
			var ev struct{ Type, Role, Text, Message string }
			if json.Unmarshal(data, &ev) == nil {
				if ev.Type == "error" {
					done <- fmt.Errorf("live conversation failed: %s", ev.Message)
					return
				}
				fmt.Fprintf(os.Stderr, "%s: %s\n", ev.Role, ev.Text)
			}
		}
	}()
	buf := make([]byte, 3200) // 100 ms of 16 kHz PCM16
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	// The input ended (a recording piped in): the server keeps listening with silence, so wait for the answer to finish
	// (nothing new for 4 s after it started, 45 s at most) before closing the conversation.
	for end := time.Now().Add(45 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if answered.Load() && time.Since(time.Unix(0, lastEvent.Load())) > 4*time.Second {
			break
		}
		select {
		case err := <-done:
			return err
		default:
		}
	}
	_ = conn.Write(ctx, websocket.MessageText, []byte("end"))
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		return nil
	}
}

// ---- usage ----

var aiUsageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Estimated AI spend of the project today and its daily cap",
	RunE: func(cmd *cobra.Command, args []string) error {
		base, err := aiBase(cmd)
		if err != nil {
			return err
		}
		var r struct {
			Used      float64 `json:"used_usd"`
			Limit     float64 `json:"limit_usd"`
			Unlimited bool    `json:"unlimited"`
			Day       string  `json:"day"`
		}
		if err := apiClient.Get(base+"/usage", &r); err != nil {
			return aiError(err)
		}
		if aiWantJSON(cmd) {
			return json.NewEncoder(os.Stdout).Encode(r)
		}
		if r.Unlimited {
			fmt.Printf("%s: $%.4f used (no daily cap)\n", r.Day, r.Used)
		} else {
			fmt.Printf("%s: $%.4f of $%.2f used\n", r.Day, r.Used, r.Limit)
		}
		return nil
	},
}

func init() {
	aiModelsCmd.Flags().String("capability", "", "Filter: chat, vision, stt, tts, speech-to-speech, image (generate+edit), image-generate, image-edit, embed, rerank, video")
	aiModelsCmd.Flags().Bool("available", false, "Only models the account can use")
	aiModelsCmd.Flags().Bool("json", false, "Print as JSON")

	for _, c := range []*cobra.Command{aiChatCmd, aiTranscribeCmd, aiJobsCmd, aiSpeakCmd, aiVoicesCmd, aiImageCmd, aiEmbedCmd, aiEmbedSearchCmd, aiRerankCmd, aiTalkCmd, aiUsageCmd} {
		addAIScope(c)
	}
	aiChatCmd.Flags().StringArray("image", nil, "Attach an image (repeatable)")
	aiChatCmd.Flags().StringArray("video", nil, "Attach a video (Nova models)")
	aiChatCmd.Flags().StringArray("document", nil, "Attach a document (pdf, txt, md, csv, docx...)")
	aiChatCmd.Flags().StringArray("audio", nil, "Attach audio: it is transcribed and the transcript joins the question")
	aiChatCmd.Flags().String("system", "", "System prompt")
	aiChatCmd.Flags().String("lang", "", "Language of attached audio (es-US, en-US...; default: detect)")
	aiChatCmd.Flags().Bool("stream", false, "Print the answer as it is written")
	aiChatCmd.Flags().Int("max-tokens", 0, "Limit of the answer's tokens")

	aiTranscribeCmd.Flags().String("lang", "", "Language code (es-US, en-US...; default: detect it)")
	aiTranscribeCmd.Flags().Bool("diarize", false, "Label who speaks")
	aiTranscribeCmd.Flags().Int("speakers", 0, "Expected number of speakers with --diarize (2-30)")
	aiTranscribeCmd.Flags().StringP("out", "o", "", "Output file; the extension picks txt, srt, vtt or json")
	aiTranscribeCmd.Flags().String("source", "", "Read the audio from a bucket object: bucket/key")
	aiTranscribeCmd.Flags().String("save-to", "", "Also store the transcript in bucket/key (a trailing / is a prefix)")
	aiTranscribeCmd.Flags().Bool("no-wait", false, "Start the job and return its id")
	aiJobsCmd.Flags().StringP("out", "o", "", "Output file for the transcript (.txt .srt .vtt .json)")
	aiJobsCmd.Flags().Bool("wait", false, "Wait until the job ends")

	aiSpeakCmd.Flags().StringP("out", "o", "", "Output audio file (.mp3, .ogg, .pcm)")
	aiSpeakCmd.Flags().String("voice", "", "Voice (see: capsule ai voices; default depends on --lang)")
	aiSpeakCmd.Flags().String("lang", "", "Language code (default es-US)")
	aiSpeakCmd.Flags().String("format", "", "mp3, ogg or pcm")
	aiSpeakCmd.Flags().String("rate", "", "Speaking rate: x-slow, slow, medium, fast, x-fast or a percentage like 90%")
	aiSpeakCmd.Flags().String("save-to", "", "Also store the audio in bucket/key (a trailing / is a prefix)")
	aiVoicesCmd.Flags().String("lang", "", "Only this language")

	aiImageCmd.Flags().StringP("out", "o", "", "Folder for the images")
	aiImageCmd.Flags().Int("n", 1, "How many images (1-4)")
	aiImageCmd.Flags().String("size", "", "Size of generated images, WxH (default 1024x1024)")
	aiImageCmd.Flags().String("format", "", "png or jpeg")
	aiImageCmd.Flags().String("image", "", "Input image to edit")
	aiImageCmd.Flags().String("image-source", "", "Input image from a bucket: bucket/key")
	aiImageCmd.Flags().String("mask", "", "Mask for inpaint (white changes, black keeps)")
	aiImageCmd.Flags().String("style-image", "", "Style reference for style-transfer")
	aiImageCmd.Flags().String("operation", "", "Edit service: inpaint, erase-object, remove-background, search-replace, search-recolor, outpaint, style-transfer, style-guide, fast-upscale, conservative-upscale, creative-upscale")
	aiImageCmd.Flags().String("search-prompt", "", "What to find (search-replace)")
	aiImageCmd.Flags().String("select-prompt", "", "What to recolor (search-recolor)")
	aiImageCmd.Flags().String("negative-prompt", "", "What to avoid")
	aiImageCmd.Flags().String("aspect-ratio", "", "Aspect ratio of style-guide results, e.g. 16:9")
	aiImageCmd.Flags().String("seed", "", "Seed for repeatable results")
	aiImageCmd.Flags().String("control-strength", "", "How much the input image guides the result (0-1)")
	aiImageCmd.Flags().String("fidelity", "", "Fidelity to the style image (0-1)")
	aiImageCmd.Flags().String("save-to", "", "Also store the images in bucket/prefix/")

	aiEmbedCmd.Flags().Int("dimensions", 0, "Vector size (model dependent: 256, 512, 1024...)")
	aiEmbedCmd.Flags().StringArray("image", nil, "Embed an image (repeatable; multimodal models)")
	aiEmbedSearchCmd.Flags().String("bucket", "", "Bucket to search")
	aiEmbedSearchCmd.Flags().String("prefix", "", "Only objects under this prefix")
	aiEmbedSearchCmd.Flags().Int("top", 5, "How many results")
	aiEmbedCmd.AddCommand(aiEmbedSearchCmd)

	aiRerankCmd.Flags().StringArray("doc", nil, "A candidate document (repeatable)")
	aiRerankCmd.Flags().String("file", "", "File with one candidate per line")
	aiRerankCmd.Flags().Int("top", 0, "Return only the best N")

	aiTalkCmd.Flags().StringP("out", "o", "", "File for the spoken answer (WAV)")
	aiTalkCmd.Flags().String("voice", "", "Voice of the answer (tiffany, matthew, amy...)")
	aiTalkCmd.Flags().String("system", "", "How the model should behave")
	aiTalkCmd.Flags().Bool("live", false, "Live conversation: PCM 16 kHz on stdin, PCM 24 kHz on stdout")

	for _, c := range []*cobra.Command{aiModelsCmd, aiChatCmd, aiTranscribeCmd, aiJobsCmd, aiSpeakCmd, aiVoicesCmd, aiImageCmd, aiEmbedCmd, aiEmbedSearchCmd, aiRerankCmd, aiTalkCmd, aiUsageCmd} {
		c.SilenceUsage = true // a failed call is not a usage mistake: no help text after the error
	}
	aiCmd.AddCommand(aiModelsCmd, aiChatCmd, aiTranscribeCmd, aiJobsCmd, aiSpeakCmd, aiVoicesCmd, aiImageCmd, aiEmbedCmd, aiRerankCmd, aiTalkCmd, aiUsageCmd)
}
