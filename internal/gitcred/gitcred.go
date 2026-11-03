// Package gitcred implements the pieces of the git-credential helper protocol
// that Capsule needs: parsing what Git writes on stdin, formatting the answer,
// and a small in-memory cache that renews a short-lived token before it
// expires. Nothing here touches the disk: a token is never persisted.
package gitcred

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RenewMargin is how close to expiry a cached token may get before a new one
// is requested (SPEC 6.5.1: renew when it lasts less than 10 minutes).
const RenewMargin = 10 * time.Minute

// Request is the key=value block Git sends to a helper.
type Request struct {
	Protocol string
	Host     string
	Path     string
	Username string
	Password string
	Other    map[string]string
}

// ParseRequest reads "key=value" lines until a blank line or EOF.
func ParseRequest(r io.Reader) (*Request, error) {
	req := &Request{Other: map[string]string{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			break
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			return nil, fmt.Errorf("malformed credential line (no key=value)")
		}
		k, v := line[:i], line[i+1:]
		switch k {
		case "protocol":
			req.Protocol = v
		case "host":
			req.Host = v
		case "path":
			req.Path = v
		case "username":
			req.Username = v
		case "password":
			req.Password = v
		default:
			req.Other[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return req, nil
}

// NormalizePath turns git's path ("org/repo.git", "/org/repo") into "org/repo".
func NormalizePath(p string) string {
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	return p
}

// Credential is what the helper answers to "get".
type Credential struct {
	Username  string
	Password  string
	ExpiresAt time.Time // zero = unknown
	RenewAt   time.Time // optional hint from the server
}

// Write formats the credential for Git. password_expiry_utc lets Git >= 2.40
// and Git Credential Manager drop the secret from their own caches on time.
func (c Credential) Write(w io.Writer) error {
	var b strings.Builder
	b.WriteString("username=" + c.Username + "\n")
	b.WriteString("password=" + c.Password + "\n")
	if !c.ExpiresAt.IsZero() {
		b.WriteString("password_expiry_utc=" + strconv.FormatInt(c.ExpiresAt.Unix(), 10) + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Valid reports whether the credential still lasts longer than margin.
func (c Credential) Valid(now time.Time, margin time.Duration) bool {
	if c.Password == "" || c.ExpiresAt.IsZero() {
		return false
	}
	if !c.RenewAt.IsZero() && !now.Before(c.RenewAt) {
		return false
	}
	return c.ExpiresAt.Sub(now) > margin
}

// Cache keeps credentials in memory only. A short-lived helper process gets
// no benefit from it across Git invocations (that is intended: nothing is
// written to disk); it matters for long-lived callers and for the tests that
// pin down the renewal rule.
type Cache struct {
	mu     sync.Mutex
	m      map[string]Credential
	Margin time.Duration
	Now    func() time.Time
}

// NewCache returns a cache with the standard 10-minute margin.
func NewCache() *Cache {
	return &Cache{m: map[string]Credential{}, Margin: RenewMargin, Now: time.Now}
}

// Key is the cache key for a host and repository path.
func Key(host, path string) string { return strings.ToLower(host) + "/" + NormalizePath(path) }

// Get returns the cached credential if it lasts more than Margin, otherwise it
// calls issue and caches the answer. reused tells which happened.
func (c *Cache) Get(key string, issue func() (Credential, error)) (cred Credential, reused bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	if cur, ok := c.m[key]; ok && cur.Valid(now, c.Margin) {
		return cur, true, nil
	}
	cred, err = issue()
	if err != nil {
		return Credential{}, false, err
	}
	c.m[key] = cred
	return cred, false, nil
}

// Forget drops one entry (git "erase").
func (c *Cache) Forget(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}
