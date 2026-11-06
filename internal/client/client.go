package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// APIError is returned for every HTTP status >= 400. Its Error() text is the
// historical "API error <status>: <code> — <message>" so callers that match on
// the text keep working; new callers use errors.As to read Status and Code.
type APIError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	Details   map[string]any
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s — %s", e.Status, e.Code, e.Message)
}

// AsAPIError unwraps err into an *APIError when there is one.
func AsAPIError(err error) (*APIError, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BaseURL returns the API origin this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// Do performs a request with optional extra headers and returns the HTTP
// status together with the decoded body (when out is not nil and the response
// has a body).
func (c *Client) Do(method, path string, body, out any, headers map[string]string) (int, error) {
	var bodyReader io.Reader
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			bodyReader = bytes.NewReader(raw)
		} else {
			b, err := json.Marshal(body)
			if err != nil {
				return 0, fmt.Errorf("marshaling request: %w", err)
			}
			bodyReader = bytes.NewReader(b)
		}
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error struct {
				Code      string         `json:"code"`
				Message   string         `json:"message"`
				RequestID string         `json:"request_id"`
				Details   map[string]any `json:"details"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return resp.StatusCode, &APIError{
			Status:    resp.StatusCode,
			Code:      apiErr.Error.Code,
			Message:   apiErr.Error.Message,
			RequestID: apiErr.Error.RequestID,
			Details:   apiErr.Error.Details,
		}
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return resp.StatusCode, fmt.Errorf("decoding response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) do(method, path string, body, out any) error {
	_, err := c.Do(method, path, body, out, nil)
	return err
}

func (c *Client) Post(path string, body, out any) error  { return c.do("POST", path, body, out) }
func (c *Client) Get(path string, out any) error         { return c.do("GET", path, nil, out) }
func (c *Client) Put(path string, body, out any) error   { return c.do("PUT", path, body, out) }
func (c *Client) Patch(path string, body, out any) error { return c.do("PATCH", path, body, out) }
func (c *Client) Delete(path string) error               { return c.do("DELETE", path, nil, nil) }

// Raw sends a request whose body and response are not JSON (multipart uploads, audio and image downloads). It uses its own timeout
// (0 = none) instead of the 30 s of the JSON calls. Statuses >= 400 become an *APIError like in Do; on success the caller owns resp.Body.
func (c *Client) Raw(method, path string, body io.Reader, headers map[string]string, timeout time.Duration) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var apiErr struct {
			Error struct {
				Code      string         `json:"code"`
				Message   string         `json:"message"`
				RequestID string         `json:"request_id"`
				Details   map[string]any `json:"details"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return nil, &APIError{Status: resp.StatusCode, Code: apiErr.Error.Code, Message: apiErr.Error.Message, RequestID: apiErr.Error.RequestID, Details: apiErr.Error.Details}
	}
	return resp, nil
}

// Token returns the bearer token the client sends ("" when there is none).
func (c *Client) Token() string { return c.token }
