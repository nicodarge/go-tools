package apptoken

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const usage = `usage: app-token mint [--client-id ID | --app-id ID] [--owner OWNER] [--repositories A,B] [--permission NAME=LEVEL ...] [--private-key-file FILE]
       app-token revoke [--token-file FILE]

Environment: APP_TOKEN_CLIENT_ID, APP_TOKEN_PRIVATE_KEY (mint), APP_TOKEN (revoke), GITHUB_API_URL,
GITHUB_REPOSITORY, GITHUB_REPOSITORY_OWNER, GITHUB_OUTPUT.`

const (
	defaultAPI = "https://api.github.com"
	timeout    = 60 * time.Second
	maxKeyFile = 64 << 10
)

var (
	namePattern       = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	permissionPattern = regexp.MustCompile(`^[a-z][a-z_-]*$`)
	levels            = map[string]bool{"read": true, "write": true, "admin": true}
)

// Env is what the command reads from and writes to. A nil Client, Now or Sleep takes the default.
type Env struct {
	Getenv func(string) string
	Stdout io.Writer
	Stderr io.Writer
	Client *http.Client
	Now    func() time.Time
	Sleep  func(context.Context, time.Duration) error
}

// Main runs the command line and returns the exit code: 0 on success, 1 on a failure, 2 on a usage
// error. Errors go to Stderr and never hold the private key or a token.
func Main(args []string, e Env) int {
	if len(args) == 0 {
		return usageError(e, "")
	}
	switch args[0] {
	case "mint":
		return runMint(args[1:], e)
	case "revoke":
		return runRevoke(args[1:], e)
	}
	return usageError(e, fmt.Sprintf("unknown command %q", args[0]))
}

func usageError(e Env, why string) int {
	if why != "" {
		_, _ = fmt.Fprintf(e.Stderr, "app-token: %s\n", why)
	}
	_, _ = fmt.Fprintln(e.Stderr, usage)
	return 2
}

func failure(e Env, err error) int {
	_, _ = fmt.Fprintf(e.Stderr, "app-token: %v\n", err)
	return 1
}

// newClient builds the API client from GITHUB_API_URL (https://api.github.com by default).
func newClient(e Env) (*client, error) {
	base := strings.TrimRight(e.Getenv("GITHUB_API_URL"), "/")
	if base == "" {
		base = defaultAPI
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("GITHUB_API_URL is not an http(s) URL")
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, errors.New("GITHUB_API_URL must be https unless its host is a loopback address")
	}
	hc := e.Client
	if hc == nil {
		hc = &http.Client{}
	}
	hc = &http.Client{
		Transport: hc.Transport,
		Timeout:   timeout,
		Jar:       hc.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	sleep := e.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	return &client{base: base, http: hc, sleep: sleep}, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

type mintFlags struct {
	clientID, appID, owner, repositories, keyFile string
	permissions                                   []string
}

func runMint(args []string, e Env) int {
	var f mintFlags
	fs := flag.NewFlagSet("mint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.clientID, "client-id", "", "")
	fs.StringVar(&f.appID, "app-id", "", "")
	fs.StringVar(&f.owner, "owner", "", "")
	fs.StringVar(&f.repositories, "repositories", "", "")
	fs.StringVar(&f.keyFile, "private-key-file", "", "")
	fs.Func("permission", "", func(v string) error {
		f.permissions = append(f.permissions, v)
		return nil
	})
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(e, "bad arguments to mint")
	}
	req, err := buildRequest(f, e)
	if err != nil {
		return failure(e, err)
	}
	pemData, err := readKey(f.keyFile, e)
	if err != nil {
		return failure(e, err)
	}
	key, err := parsePrivateKey(pemData)
	if err != nil {
		return failure(e, err)
	}
	c, err := newClient(e)
	if err != nil {
		return failure(e, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	got, err := mint(ctx, c, key, req, e.now())
	if err != nil {
		return failure(e, err)
	}
	if err := publish(got, e); err != nil {
		revokeToken(c, got.token, e)
		return failure(e, err)
	}
	if got.slugErr != nil {
		_, _ = fmt.Fprintf(e.Stderr, "::warning::app-token: the app-slug output is empty: %v\n", got.slugErr)
	}
	return 0
}

// buildRequest resolves the flags and the Actions environment the way the action does: with neither
// an owner nor repositories, the token covers the current repository; with repositories and no
// owner, the current owner's; with an owner alone, every repository the installation holds.
func buildRequest(f mintFlags, e Env) (request, error) {
	iss, err := issuer(f, e)
	if err != nil {
		return request{}, err
	}
	owner := f.owner
	if owner != "" && !validName(owner) {
		return request{}, errors.New("--owner is not a GitHub account name")
	}
	if owner == "" {
		if owner, err = currentOwner(e); err != nil {
			return request{}, err
		}
	}
	repos, err := splitRepositories(f.repositories, owner)
	if err != nil {
		return request{}, fmt.Errorf("--repositories: %w", err)
	}
	if f.owner == "" && len(repos) == 0 {
		_, repo, repoErr := currentRepository(e)
		if repoErr != nil {
			return request{}, repoErr
		}
		if repo == "" {
			return request{}, errors.New("no --owner, no --repositories and no GITHUB_REPOSITORY")
		}
		repos = []string{repo}
	}
	perms, err := parsePermissions(f.permissions)
	if err != nil {
		return request{}, err
	}
	return request{iss: iss, owner: owner, repos: repos, permissions: perms}, nil
}

func issuer(f mintFlags, e Env) (string, error) {
	if f.clientID != "" && f.appID != "" {
		return "", errors.New("--client-id and --app-id are exclusive")
	}
	iss := f.clientID
	if iss == "" {
		iss = f.appID
	}
	if iss == "" {
		iss = strings.TrimSpace(e.Getenv("APP_TOKEN_CLIENT_ID"))
	}
	if iss == "" {
		return "", errors.New("no client ID: set --client-id, --app-id or APP_TOKEN_CLIENT_ID")
	}
	if !namePattern.MatchString(iss) {
		return "", errors.New("the client ID or App ID has an unexpected character")
	}
	return iss, nil
}

// validName accepts the characters of a GitHub account or repository name, and refuses "." and "..".
func validName(name string) bool {
	return namePattern.MatchString(name) && name != "." && name != ".."
}

// currentOwner is GITHUB_REPOSITORY_OWNER, else the owner of GITHUB_REPOSITORY.
func currentOwner(e Env) (string, error) {
	if owner := e.Getenv("GITHUB_REPOSITORY_OWNER"); owner != "" {
		if !validName(owner) {
			return "", errors.New("GITHUB_REPOSITORY_OWNER is not a GitHub account name")
		}
		return owner, nil
	}
	owner, _, err := currentRepository(e)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return "", errors.New("no --owner and no GITHUB_REPOSITORY_OWNER or GITHUB_REPOSITORY")
	}
	return owner, nil
}

// currentRepository splits GITHUB_REPOSITORY into the owner and the repository name; both are empty
// when it is unset.
func currentRepository(e Env) (owner, repo string, err error) {
	slug := e.Getenv("GITHUB_REPOSITORY")
	if slug == "" {
		return "", "", nil
	}
	owner, repo, ok := strings.Cut(slug, "/")
	if !ok || !validName(owner) || !validName(repo) {
		return "", "", errors.New("GITHUB_REPOSITORY is not OWNER/REPOSITORY")
	}
	return owner, repo, nil
}

// splitRepositories splits a list separated by commas and newlines, as the action does. An item is a
// repository name or OWNER/NAME, whose OWNER must be owner.
func splitRepositories(list, owner string) ([]string, error) {
	var names []string
	for _, item := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == '\n' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name := item
		if itemOwner, rest, ok := strings.Cut(item, "/"); ok {
			if !validName(itemOwner) {
				return nil, fmt.Errorf("%q is not a repository name", item)
			}
			if !strings.EqualFold(itemOwner, owner) {
				return nil, fmt.Errorf("%q names owner %q, which does not match the owner %q", item, itemOwner, owner)
			}
			name = rest
		}
		if !validName(name) {
			return nil, fmt.Errorf("%q is not a repository name", item)
		}
		names = append(names, name)
	}
	return names, nil
}

// parsePermissions reads NAME=LEVEL pairs. A name is lowercased and a hyphen in it becomes an
// underscore, so the names of the action's permission-* inputs (pull-requests) and the API's
// (pull_requests) both work.
func parsePermissions(list []string) (map[string]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	perms := make(map[string]string, len(list))
	for _, item := range list {
		name, level, ok := strings.Cut(item, "=")
		name = strings.ToLower(name)
		if !ok || !permissionPattern.MatchString(name) || !levels[level] {
			return nil, fmt.Errorf("--permission %q: want NAME=read|write|admin", item)
		}
		name = strings.ReplaceAll(name, "-", "_")
		if prev, dup := perms[name]; dup && prev != level {
			return nil, fmt.Errorf("--permission %q is given twice with different levels", name)
		}
		perms[name] = level
	}
	return perms, nil
}

// readKey returns the PEM private key from APP_TOKEN_PRIVATE_KEY or from keyFile, never from
// the command line.
func readKey(keyFile string, e Env) ([]byte, error) {
	fromEnv := e.Getenv("APP_TOKEN_PRIVATE_KEY")
	switch {
	case fromEnv != "" && keyFile != "":
		return nil, errors.New("APP_TOKEN_PRIVATE_KEY and --private-key-file are exclusive")
	case fromEnv != "":
		return []byte(fromEnv), nil
	case keyFile == "":
		return nil, errors.New("no private key: set APP_TOKEN_PRIVATE_KEY or --private-key-file")
	}
	data, err := readFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("reading the private key file: %w", err)
	}
	return data, nil
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxKeyFile {
		return nil, errors.New("the file is too large")
	}
	return data, nil
}

// publish masks the token, then exposes the outputs: to $GITHUB_OUTPUT when it is set, to the
// standard output otherwise. The mask line is the first thing written anywhere.
func publish(got minted, e Env) error {
	if _, err := fmt.Fprintf(e.Stdout, "::add-mask::%s\n", got.token); err != nil {
		return errors.New("the mask command cannot be written")
	}
	lines := fmt.Sprintf("token=%s\ninstallation-id=%d\n", got.token, got.installationID)
	if got.slug != "" {
		lines += "app-slug=" + got.slug + "\n"
	}
	if path := e.Getenv("GITHUB_OUTPUT"); path != "" {
		if err := appendFile(path, lines); err != nil {
			return fmt.Errorf("writing GITHUB_OUTPUT: %w", err)
		}
		return nil
	}
	if _, err := io.WriteString(e.Stdout, lines); err != nil {
		return errors.New("the outputs cannot be written")
	}
	return nil
}

func appendFile(path, content string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func runRevoke(args []string, e Env) int {
	var tokenFile string
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&tokenFile, "token-file", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(e, "bad arguments to revoke")
	}
	token := strings.TrimSpace(e.Getenv("APP_TOKEN"))
	if tokenFile != "" {
		if token != "" {
			return failure(e, errors.New("APP_TOKEN and --token-file are exclusive"))
		}
		data, err := readFile(tokenFile)
		if err != nil {
			return failure(e, fmt.Errorf("reading the token file: %w", err))
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		_, _ = fmt.Fprintln(e.Stdout, "::notice::app-token: no token to revoke")
		return 0
	}
	if err := checkToken(token); err != nil {
		return failure(e, errors.New("the token to revoke holds a white space or control character"))
	}
	c, err := newClient(e)
	if err != nil {
		return failure(e, err)
	}
	revokeToken(c, token, e)
	return 0
}

// revokeToken deletes the installation token. Like the action's post step, it never fails the step:
// a failure is a warning, and a 401 (revoked or expired already) a notice.
func revokeToken(c *client, token string, e Env) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	gone, err := revoke(ctx, c, token)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(e.Stderr, "::warning::app-token: token revocation failed: %s\n", oneLine(err.Error()))
	case gone:
		_, _ = fmt.Fprintln(e.Stdout, "::notice::app-token: the token was already revoked or expired")
	}
}

// oneLine replaces every control character, so a message cannot start a second workflow command.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
