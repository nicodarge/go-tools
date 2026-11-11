package apptoken

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"
	"unicode"
)

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// request is what a token is asked for.
type request struct {
	iss         string
	owner       string
	repos       []string
	permissions map[string]string
}

// minted is an installation token and what the outputs expose about it.
type minted struct {
	token          string
	installationID int64
	slug           string
	// slugErr is why slug is empty; it is not fatal.
	slugErr error
}

// installation is what the installation lookup answers.
type installation struct {
	ID   int64  `json:"id"`
	Slug string `json:"app_slug"`
}

const (
	retries    = 3
	retryFirst = time.Second
)

// mint authenticates as the App, finds the installation of the owner (or of the first repository),
// and asks for a token scoped to the repositories and permissions of req. The lookup and the token
// request are retried together on a 5xx or a transport error, as the action does.
func mint(ctx context.Context, c *client, key *rsa.PrivateKey, req request, now time.Time) (minted, error) {
	jwt, err := signJWT(key, req.iss, now)
	if err != nil {
		return minted{}, err
	}
	body := map[string]any{}
	if len(req.repos) > 0 {
		body["repositories"] = req.repos
	}
	if len(req.permissions) > 0 {
		body["permissions"] = req.permissions
	}
	var inst installation
	var created struct {
		Token string `json:"token"`
	}
	err = withRetry(ctx, c, func() error {
		found, lookupErr := findInstallation(ctx, c, jwt, req)
		if lookupErr != nil {
			return fmt.Errorf("finding the installation of %s: %w", target(req), lookupErr)
		}
		inst = found
		path := fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID)
		if postErr := c.do(ctx, http.MethodPost, path, jwt, body, &created, http.StatusCreated); postErr != nil {
			return fmt.Errorf("creating the installation token: %w", postErr)
		}
		return nil
	})
	if err != nil {
		return minted{}, err
	}
	if err := checkToken(created.Token); err != nil {
		return minted{}, err
	}
	out := minted{token: created.Token, installationID: inst.ID}
	if slugPattern.MatchString(inst.Slug) {
		out.slug = inst.Slug
	} else {
		out.slugErr = errors.New("the installation answer carries no usable app_slug")
	}
	return out, nil
}

// withRetry runs fn, and again after 1s, 2s and 4s while it fails on a 5xx or a transport error,
// until a wait is cut short by ctx. A 4xx is never retried.
func withRetry(ctx context.Context, c *client, fn func() error) error {
	delay := retryFirst
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || attempt == retries || !retryable(err) {
			return err
		}
		if c.sleep(ctx, delay) != nil {
			return err
		}
		delay *= 2
	}
}

func retryable(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.status >= http.StatusInternalServerError
	}
	var transport *transportError
	return errors.As(err, &transport)
}

func target(req request) string {
	if len(req.repos) > 0 {
		return req.owner + "/" + req.repos[0]
	}
	return req.owner
}

// findInstallation looks the installation up on the first repository when repositories are named,
// and on the owner otherwise (an organization first, then a user).
func findInstallation(ctx context.Context, c *client, jwt string, req request) (installation, error) {
	var found installation
	if len(req.repos) > 0 {
		path := "/repos/" + url.PathEscape(req.owner) + "/" + url.PathEscape(req.repos[0]) + "/installation"
		if err := c.do(ctx, http.MethodGet, path, jwt, nil, &found, http.StatusOK); err != nil {
			return installation{}, err
		}
		return found, validID(found.ID)
	}
	err := c.do(ctx, http.MethodGet, "/orgs/"+url.PathEscape(req.owner)+"/installation", jwt, nil, &found, http.StatusOK)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound {
		err = c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(req.owner)+"/installation", jwt, nil, &found, http.StatusOK)
	}
	if err != nil {
		return installation{}, err
	}
	return found, validID(found.ID)
}

func validID(id int64) error {
	if id <= 0 {
		return errors.New("the installation answer carries no id")
	}
	return nil
}

// checkToken refuses a token that is empty or holds white space or a control character: it is
// written to a line-based output file and to a workflow command.
func checkToken(token string) error {
	if token == "" {
		return errors.New("the token answer carries no token")
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("the token answer carries a token with a white space or control character")
		}
	}
	return nil
}

// revoke deletes the installation token itself. A 401 means it is already revoked or expired.
func revoke(ctx context.Context, c *client, token string) (alreadyGone bool, err error) {
	err = c.do(ctx, http.MethodDelete, "/installation/token", token, nil, nil, http.StatusNoContent)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusUnauthorized {
		return true, nil
	}
	return false, err
}
