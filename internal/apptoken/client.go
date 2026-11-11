package apptoken

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	apiVersion = "2022-11-28"
	// maxBody bounds what is read from any answer.
	maxBody = 1 << 20
	// maxMessage bounds the GitHub error message echoed back.
	maxMessage = 200
)

// client talks to the GitHub REST API at base.
type client struct {
	base string
	http *http.Client
}

// apiError is a status the call did not expect.
type apiError struct {
	method  string
	path    string
	status  int
	message string
}

func (e *apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("%s %s: HTTP %d", e.method, e.path, e.status)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.method, e.path, e.status, e.message)
}

// do sends the request with the bearer credential and decodes the JSON answer into out (when not
// nil) if the status is want. A redirect is never followed, so the credential goes nowhere else.
func (c *client) do(ctx context.Context, method, path, bearer string, body, out any, want int) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("%s %s: bad request", method, path)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "app-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: request failed: %w", method, path, unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%s %s: reading the answer: %w", method, path, err)
	}
	if resp.StatusCode != want {
		return &apiError{method: method, path: path, status: resp.StatusCode, message: errorMessage(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: the answer is not the expected JSON", method, path)
	}
	return nil
}

// unwrapURLError drops the *url.Error wrapper, whose text repeats the whole request URL.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// errorMessage extracts the `message` member of a GitHub error answer.
func errorMessage(raw []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return ""
	}
	msg := strings.Join(strings.Fields(parsed.Message), " ")
	if len(msg) > maxMessage {
		msg = msg[:maxMessage]
	}
	return msg
}
