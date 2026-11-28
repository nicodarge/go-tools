package netboxcablesync

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout = 60 * time.Second
	maxBody        = 32 << 20
	maxMessage     = 200
	maxCAFile      = 1 << 20
	pageSize       = 1000
)

// apiClient talks to one HTTP API at base. auth adds the credential to every request; a redirect is
// never followed, so the credential goes nowhere else.
type apiClient struct {
	name string
	base string
	http *http.Client
	auth func(*http.Request)
}

type statusError struct {
	name    string
	method  string
	path    string
	status  int
	message string
}

func (e *statusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("%s %s %s: HTTP %d", e.name, e.method, e.path, e.status)
	}
	return fmt.Sprintf("%s %s %s: HTTP %d: %s", e.name, e.method, e.path, e.status, e.message)
}

// newAPIClient validates rawURL (http(s), no credentials, https unless loopback) and builds a client
// that trusts the CA bundle in caFile on top of the system roots. A non-nil shared client replaces
// the transport, which is how the tests reach their fakes.
func newAPIClient(name, rawURL, caFile string, shared *http.Client, auth func(*http.Request)) (*apiClient, error) {
	base := strings.TrimRight(rawURL, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("the %s URL is not an http(s) URL without credentials", name)
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("the %s URL must be https unless its host is a loopback address", name)
	}
	var transport http.RoundTripper
	if caFile != "" {
		var tr *http.Transport
		if tr, err = caTransport(caFile); err != nil {
			return nil, fmt.Errorf("the %s CA file: %w", name, err)
		}
		transport = tr
	}
	if shared != nil {
		transport = shared.Transport
	}
	hc := &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &apiClient{name: name, base: base, http: hc, auth: auth}, nil
}

func caTransport(caFile string) (*http.Transport, error) {
	f, err := os.Open(filepath.Clean(caFile))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	pemData, err := io.ReadAll(io.LimitReader(f, maxCAFile))
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, errors.New("no certificate in the file")
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("unexpected default transport")
	}
	tr = tr.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return tr, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// do sends the request and decodes the JSON answer into out (when not nil) if the status is one of
// want. Errors name the method, the path and the status, never a header.
func (c *apiClient) do(ctx context.Context, method, path string, body, out any, want ...int) error {
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
		return fmt.Errorf("%s %s %s: bad request", c.name, method, path)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "netbox-cable-sync")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%s %s %s: request failed: %w", c.name, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%s %s %s: reading the answer: %w", c.name, method, path, err)
	}
	for _, w := range want {
		if resp.StatusCode == w {
			if out == nil || len(raw) == 0 {
				return nil
			}
			if err = json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("%s %s %s: the answer is not the expected JSON", c.name, method, path)
			}
			return nil
		}
	}
	return &statusError{name: c.name, method: method, path: path, status: resp.StatusCode, message: errorMessage(raw)}
}

func errorMessage(raw []byte) string {
	var body struct {
		Detail  string `json:"detail"`
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(raw, &body) == nil {
		msg = body.Detail
		if msg == "" {
			msg = body.Message
		}
	}
	if msg == "" {
		msg = string(raw)
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + "..."
	}
	return msg
}

type page[T any] struct {
	Count   int `json:"count"`
	Results []T `json:"results"`
}

// listAll reads every page of a NetBox list endpoint. path may carry a query.
func listAll[T any](ctx context.Context, c *apiClient, path string) ([]T, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var all []T
	for offset := 0; ; {
		var p page[T]
		err := c.do(ctx, http.MethodGet, path+sep+"limit="+strconv.Itoa(pageSize)+"&offset="+strconv.Itoa(offset), nil, &p, http.StatusOK)
		if err != nil {
			return nil, err
		}
		all = append(all, p.Results...)
		offset += len(p.Results)
		if len(p.Results) == 0 || offset >= p.Count {
			return all, nil
		}
	}
}
