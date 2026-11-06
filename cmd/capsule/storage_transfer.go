package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Uploads up to singlePutLimit go as one presigned PUT; larger files use multipart with a local state
// file, so an interrupted `capsule storage put` resumes where it stopped (only missing parts are sent).
const singlePutLimit = 16 << 20

// transferHTTP has no overall timeout (large files); connection setup and idle phases are bounded.
var transferHTTP = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 10 * time.Minute,
	IdleConnTimeout:       30 * time.Second,
}}

type objInfo struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ETag        string    `json:"etag"`
	ContentType string    `json:"content_type"`
	Modified    time.Time `json:"modified"`
}

type listPage struct {
	Objects   []objInfo `json:"objects"`
	Prefixes  []string  `json:"prefixes"`
	Truncated bool      `json:"truncated"`
	Next      string    `json:"next"`
}

// listObjects pages through the whole listing.
func listObjects(orgID, projectID string, b *storageItem, prefix, delimiter string) ([]objInfo, []string, error) {
	var objs []objInfo
	var prefixes []string
	after := ""
	for {
		q := url.Values{"prefix": {prefix}, "delimiter": {delimiter}, "after": {after}, "limit": {"1000"}}
		var pg listPage
		if err := apiClient.Get(storageBase(orgID, projectID)+"/"+b.ID+"/objects?"+q.Encode(), &pg); err != nil {
			return nil, nil, err
		}
		objs = append(objs, pg.Objects...)
		prefixes = append(prefixes, pg.Prefixes...)
		if !pg.Truncated || pg.Next == "" {
			return objs, prefixes, nil
		}
		after = pg.Next
	}
}

var storageLsCmd = &cobra.Command{
	Use:   "ls <bucket>[/prefix]",
	Short: "List objects (folders by default; -r lists everything under the prefix)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, prefix, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		delim := "/"
		if recursive {
			delim = ""
		}
		objs, prefixes, err := listObjects(orgID, projectID, b, prefix, delim)
		if err != nil {
			return err
		}
		if out, _ := cmd.Flags().GetString("output"); out == "json" {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"objects": objs, "prefixes": prefixes})
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		sort.Strings(prefixes)
		for _, p := range prefixes {
			fmt.Fprintf(w, "DIR\t-\t-\t%s\n", p)
		}
		for _, o := range objs {
			fmt.Fprintf(w, "FILE\t%s\t%s\t%s\n", humanBytes(o.Size), o.Modified.Local().Format("2006-01-02 15:04"), o.Key)
		}
		return w.Flush()
	},
}

var storagePutCmd = &cobra.Command{
	Use:   "put <file|dir|-> <bucket>/<key>",
	Short: "Upload a file (large files are multipart and resumable); -r uploads a directory",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, key, err := ownBucketFor(cmd, args[1])
		if err != nil {
			return err
		}
		src := args[0]
		recursive, _ := cmd.Flags().GetBool("recursive")
		ctype, _ := cmd.Flags().GetString("content-type")
		partSize, _ := cmd.Flags().GetString("part-size")
		ps, err := parseSize(partSize)
		if err != nil {
			return err
		}
		if src == "-" {
			return putStdin(orgID, projectID, b, key, ctype)
		}
		st, err := os.Stat(src)
		if err != nil {
			return err
		}
		if st.IsDir() {
			if !recursive {
				return fmt.Errorf("%s is a directory: use -r", src)
			}
			n := 0
			err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(src, p)
				k := path.Join(strings.TrimSuffix(key, "/"), filepath.ToSlash(rel))
				n++
				return uploadFile(orgID, projectID, b, p, k, "", ps)
			})
			if err == nil {
				fmt.Printf("%d files uploaded to %s\n", n, b.Name)
			}
			return err
		}
		if key == "" || strings.HasSuffix(key, "/") {
			key += filepath.Base(src)
		}
		return uploadFile(orgID, projectID, b, src, key, ctype, ps)
	},
}

func contentTypeFor(file, override string) string {
	if override != "" {
		return override
	}
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(file))); t != "" {
		return t
	}
	return "application/octet-stream"
}

func putStdin(orgID, projectID string, b *storageItem, key, ctype string) error {
	if key == "" || strings.HasSuffix(key, "/") {
		return fmt.Errorf("a key is required when reading from stdin")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, singlePutLimit+1))
	if err != nil {
		return err
	}
	if len(data) > singlePutLimit {
		return fmt.Errorf("stdin is larger than %s: write it to a file first so the upload can be resumed", humanBytes(singlePutLimit))
	}
	return putSingle(orgID, projectID, b, key, contentTypeFor(key, ctype), bytes.NewReader(data), int64(len(data)))
}

func httpPut(u string, body io.Reader, size int64, ctype string) (http.Header, error) {
	req, err := http.NewRequest(http.MethodPut, u, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := transferHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("storage answered %d: %s", resp.StatusCode, s3ErrorText(msg))
	}
	return resp.Header, nil
}

// s3ErrorText extracts <Code>: <Message> from an S3 error body.
func s3ErrorText(b []byte) string {
	s := string(b)
	get := func(tag string) string {
		i, j := strings.Index(s, "<"+tag+">"), strings.Index(s, "</"+tag+">")
		if i < 0 || j < i {
			return ""
		}
		return s[i+len(tag)+2 : j]
	}
	if c := get("Code"); c != "" {
		return c + ": " + get("Message")
	}
	return strings.TrimSpace(s)
}

func putSingle(orgID, projectID string, b *storageItem, key, ctype string, body io.Reader, size int64) error {
	u, err := presignURL(orgID, projectID, b.ID, key, "PUT", 3600)
	if err != nil {
		return err
	}
	if _, err := httpPut(u, body, size, ctype); err != nil {
		return err
	}
	fmt.Printf("%s/%s  %s\n", b.Name, key, humanBytes(size))
	return nil
}

// ── resumable multipart ─────────────────────────────────────────────────────

type uploadState struct {
	UploadID string         `json:"upload_id"`
	Key      string         `json:"key"`
	Size     int64          `json:"size"`
	ModTime  int64          `json:"mod_time"`
	PartSize int64          `json:"part_size"`
	ETags    map[int]string `json:"etags"`
}

type uploadPart struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
}

type uploadStart struct {
	UploadID string       `json:"upload_id"`
	PartSize int64        `json:"part_size"`
	Parts    []uploadPart `json:"parts"`
}

func stateFile(orgID, projectID, bucketID, key, file string, size, mod int64) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	abs, _ := filepath.Abs(file)
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%d|%d", orgID, projectID, bucketID, key, abs, size, mod)))
	return filepath.Join(dir, "capsule", "uploads", hex.EncodeToString(h[:12])+".json")
}

func loadState(p string) *uploadState {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var s uploadState
	if json.Unmarshal(raw, &s) != nil || s.UploadID == "" {
		return nil
	}
	if s.ETags == nil {
		s.ETags = map[int]string{}
	}
	return &s
}

func saveState(p string, s *uploadState) {
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	raw, _ := json.Marshal(s)
	_ = os.WriteFile(p, raw, 0o600)
}

func uploadFile(orgID, projectID string, b *storageItem, file, key, ctype string, partSize int64) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	ct := contentTypeFor(file, ctype)
	if st.Size() <= singlePutLimit {
		return putSingle(orgID, projectID, b, key, ct, f, st.Size())
	}
	sf := stateFile(orgID, projectID, b.ID, key, file, st.Size(), st.ModTime().UnixNano())
	base := storageBase(orgID, projectID) + "/" + b.ID
	state := loadState(sf)
	req := map[string]any{"key": key, "content_type": ct, "size": st.Size()}
	if partSize > 0 {
		req["part_size"] = partSize
	}
	var start uploadStart
	resumed := false
	have := map[int]int64{}
	if state != nil {
		var cur struct {
			Parts []struct {
				Number int    `json:"number"`
				Size   int64  `json:"size"`
				ETag   string `json:"etag"`
			} `json:"parts"`
		}
		if err := apiClient.Get(base+"/uploads/"+state.UploadID, &cur); err == nil {
			req["upload_id"] = state.UploadID
			req["part_size"] = state.PartSize
			resumed = true
			for _, p := range cur.Parts {
				have[p.Number] = p.Size
				state.ETags[p.Number] = p.ETag
			}
		} else {
			state = nil // the server forgot it (expired/aborted): start over
		}
	}
	if err := apiClient.Post(base+"/uploads", req, &start); err != nil {
		return err
	}
	if !resumed {
		state = &uploadState{UploadID: start.UploadID, Key: key, Size: st.Size(), ModTime: st.ModTime().UnixNano(), PartSize: start.PartSize, ETags: map[int]string{}}
		saveState(sf, state)
	}
	var offset int64
	sent, skipped := 0, 0
	for _, p := range start.Parts {
		if sz, ok := have[p.Number]; ok && sz == p.Size && state.ETags[p.Number] != "" {
			offset += p.Size
			skipped++
			continue
		}
		var h http.Header
		err := retry(3, func() error {
			var e error
			h, e = httpPut(p.URL, io.NewSectionReader(f, offset, p.Size), p.Size, "")
			return e
		})
		if err != nil {
			if strings.Contains(err.Error(), "QuotaExceeded") { // resuming cannot help: free the parts already stored and forget the upload
				_ = apiClient.Delete(base + "/uploads/" + state.UploadID)
				_ = os.Remove(sf)
				return fmt.Errorf("part %d/%d: %w (the partial upload was discarded)", p.Number, len(start.Parts), err)
			}
			return fmt.Errorf("part %d/%d failed (run the same command again to resume): %w", p.Number, len(start.Parts), err)
		}
		state.ETags[p.Number] = strings.Trim(h.Get("ETag"), `"`)
		saveState(sf, state)
		offset += p.Size
		sent++
		fmt.Fprintf(os.Stderr, "\r%s: %d/%d parts", key, p.Number, len(start.Parts))
	}
	fmt.Fprintln(os.Stderr)
	parts := make([]map[string]any, 0, len(start.Parts))
	for _, p := range start.Parts {
		parts = append(parts, map[string]any{"number": p.Number, "etag": state.ETags[p.Number]})
	}
	var obj objInfo
	if err := apiClient.Post(base+"/uploads/"+state.UploadID+"/complete", map[string]any{"parts": parts}, &obj); err != nil {
		return err
	}
	_ = os.Remove(sf)
	note := ""
	if resumed {
		note = fmt.Sprintf(" (resumed: %d parts already there, %d sent)", skipped, sent)
	}
	fmt.Printf("%s/%s  %s%s\n", b.Name, key, humanBytes(obj.Size), note)
	return nil
}

func retry(n int, fn func() error) error {
	var err error
	for i := 0; i < n; i++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
	}
	return err
}

// ── download ────────────────────────────────────────────────────────────────

var storageGetCmd = &cobra.Command{
	Use:   "get <bucket>/<key> [dest]",
	Short: "Download an object (an interrupted download resumes; '-' writes to stdout)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, key, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		if key == "" {
			return fmt.Errorf("expected <bucket>/<key>")
		}
		dest := path.Base(key)
		if len(args) == 2 {
			dest = args[1]
		}
		u, err := presignURL(orgID, projectID, b.ID, key, "GET", 3600)
		if err != nil {
			return err
		}
		if dest == "-" {
			return downloadTo(u, os.Stdout)
		}
		if st, err := os.Stat(dest); err == nil && st.IsDir() {
			dest = filepath.Join(dest, path.Base(key))
		}
		n, err := downloadResumable(u, dest)
		if err != nil {
			return err
		}
		fmt.Printf("%s  %s\n", dest, humanBytes(n))
		return nil
	},
}

func downloadTo(u string, w io.Writer) error {
	resp, err := transferHTTP.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("storage answered %d: %s", resp.StatusCode, s3ErrorText(msg))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// downloadResumable writes to dest+".part" (kept on failure) and continues it with a Range request;
// If-Range makes the server resend everything when the object changed in between.
func downloadResumable(u, dest string) (int64, error) {
	part := dest + ".part"
	etagFile := part + ".etag"
	var have int64
	var etag string
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
		raw, _ := os.ReadFile(etagFile)
		etag = strings.TrimSpace(string(raw))
	}
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if have > 0 && etag != "" {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		req.Header.Set("If-Range", etag)
	} else {
		have = 0
	}
	resp, err := transferHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		flags |= os.O_TRUNC
		have = 0
	case http.StatusRequestedRangeNotSatisfiable: // already complete
		return have, os.Rename(part, dest)
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("storage answered %d: %s", resp.StatusCode, s3ErrorText(msg))
	}
	if e := resp.Header.Get("ETag"); e != "" {
		_ = os.WriteFile(etagFile, []byte(e), 0o600)
	}
	if dir := filepath.Dir(dest); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	total := have + n
	if err != nil {
		return total, fmt.Errorf("download interrupted after %s (run the same command again to resume): %w", humanBytes(total), err)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return total, errors.New("download ended early (run the same command again to resume)")
	}
	_ = os.Remove(etagFile)
	return total, os.Rename(part, dest)
}

var storageRmCmd = &cobra.Command{
	Use:   "rm <bucket>/<key>",
	Short: "Delete an object (-r deletes everything under a prefix)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orgID, projectID, b, key, err := ownBucketFor(cmd, args[0])
		if err != nil {
			return err
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		del := func(k string) error {
			return apiClient.Delete(storageBase(orgID, projectID) + "/" + b.ID + "/objects?key=" + url.QueryEscape(k))
		}
		if !recursive {
			if key == "" {
				return fmt.Errorf("expected <bucket>/<key> (or -r with a prefix)")
			}
			if err := del(key); err != nil {
				return err
			}
			fmt.Println("deleted", b.Name+"/"+key)
			return nil
		}
		objs, _, err := listObjects(orgID, projectID, b, key, "")
		if err != nil {
			return err
		}
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !confirm(fmt.Sprintf("Delete %d objects under %s/%s?", len(objs), b.Name, key), false) {
			return nil
		}
		for _, o := range objs {
			if err := del(o.Key); err != nil {
				return err
			}
		}
		fmt.Printf("%d objects deleted\n", len(objs))
		return nil
	},
}

func init() {
	storageLsCmd.Flags().BoolP("recursive", "r", false, "List every object under the prefix, not just one level")
	storagePutCmd.Flags().BoolP("recursive", "r", false, "Upload a directory")
	storagePutCmd.Flags().String("content-type", "", "Content-Type (default: from the file extension)")
	storagePutCmd.Flags().String("part-size", "16MB", "Part size of multipart uploads (min 5MB)")
	storageRmCmd.Flags().BoolP("recursive", "r", false, "Delete everything under the prefix")
	storageRmCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	storageCmd.AddCommand(storageLsCmd, storagePutCmd, storageGetCmd, storageRmCmd)
}
