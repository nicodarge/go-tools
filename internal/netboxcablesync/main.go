// Package netboxcablesync derives the physical cables of a network from a UniFi Network controller
// (LLDP neighbours and wired clients) and keeps them in NetBox. It plans first and writes only on
// request; the cables it creates carry a tag, and it never modifies or deletes one without it.
package netboxcablesync

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const usage = `usage: netbox-cable-sync [--apply] [--prune] [--unifi-url URL] [--unifi-site SITE] [--unifi-api-path PATH]
                         [--unifi-ca-file FILE] [--netbox-url URL] [--netbox-ca-file FILE] [--tag SLUG]

Environment: UNIFI_API_KEY, NETBOX_TOKEN (secrets, never flags); UNIFI_URL, UNIFI_SITE, UNIFI_API_PATH,
UNIFI_CA_FILE, NETBOX_URL, NETBOX_CA_FILE, NETBOX_CABLE_TAG (defaults of the flags; an empty variable is
unset, an empty --unifi-api-path selects a standalone controller).`

const (
	defaultSite = "default"
	defaultPath = "/proxy/network"
	defaultTag  = "unifi-sync"
	runTimeout  = 10 * time.Minute
)

var errUsage = errors.New("see the usage")

var (
	tagPattern  = regexp.MustCompile(`^[-a-zA-Z0-9_]+$`)
	sitePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	pathPattern = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)*$`)
)

// Env is what the command reads from and writes to. A non-nil Client supplies the HTTP transport.
type Env struct {
	Getenv func(string) string
	Stdout io.Writer
	Stderr io.Writer
	Client *http.Client
}

type config struct {
	unifiURL, site, apiPath, unifiCA string
	netboxURL, netboxCA, tag         string
	apply, prune                     bool
	unifiKey, netboxToken            string
}

// Main runs the command line and returns the exit code: 0 when the plan was made (and applied, with
// --apply), 1 when a write failed, 2 on a configuration or API read error.
func Main(args []string, e Env) int {
	cfg, err := parseConfig(args, e)
	if err != nil {
		_, _ = fmt.Fprintf(e.Stderr, "netbox-cable-sync: %v\n", err)
		if errors.Is(err, errUsage) {
			_, _ = fmt.Fprintln(e.Stderr, usage)
		}
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	return run(ctx, cfg, e)
}

func parseConfig(args []string, e Env) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("netbox-cable-sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.unifiURL, "unifi-url", e.Getenv("UNIFI_URL"), "")
	fs.StringVar(&cfg.site, "unifi-site", envOr(e, "UNIFI_SITE", defaultSite), "")
	fs.StringVar(&cfg.apiPath, "unifi-api-path", envOr(e, "UNIFI_API_PATH", defaultPath), "")
	fs.StringVar(&cfg.unifiCA, "unifi-ca-file", e.Getenv("UNIFI_CA_FILE"), "")
	fs.StringVar(&cfg.netboxURL, "netbox-url", e.Getenv("NETBOX_URL"), "")
	fs.StringVar(&cfg.netboxCA, "netbox-ca-file", e.Getenv("NETBOX_CA_FILE"), "")
	fs.StringVar(&cfg.tag, "tag", envOr(e, "NETBOX_CABLE_TAG", defaultTag), "")
	fs.BoolVar(&cfg.apply, "apply", false, "")
	fs.BoolVar(&cfg.prune, "prune", false, "")
	if err := fs.Parse(args); err != nil {
		return cfg, fmt.Errorf("bad arguments: %w", errUsage)
	}
	if fs.NArg() != 0 {
		return cfg, fmt.Errorf("unexpected argument: %w", errUsage)
	}
	cfg.unifiKey = e.Getenv("UNIFI_API_KEY")
	cfg.netboxToken = e.Getenv("NETBOX_TOKEN")
	cfg.apiPath = strings.TrimRight(cfg.apiPath, "/")
	switch {
	case cfg.unifiURL == "":
		return cfg, errors.New("no UniFi URL: set UNIFI_URL or --unifi-url")
	case cfg.netboxURL == "":
		return cfg, errors.New("no NetBox URL: set NETBOX_URL or --netbox-url")
	case cfg.unifiKey == "":
		return cfg, errors.New("no UniFi API key: set UNIFI_API_KEY")
	case cfg.netboxToken == "":
		return cfg, errors.New("no NetBox token: set NETBOX_TOKEN")
	case !plainSegment(cfg.site):
		return cfg, errors.New("the UniFi site is not a plain site name")
	case !plainPath(cfg.apiPath):
		return cfg, errors.New("the UniFi API path must be empty or start with / and hold plain path segments")
	case !tagPattern.MatchString(cfg.tag):
		return cfg, errors.New("the tag must be a NetBox slug")
	}
	return cfg, nil
}

func plainSegment(s string) bool {
	return s != "." && s != ".." && sitePattern.MatchString(s)
}

func plainPath(p string) bool {
	if !pathPattern.MatchString(p) {
		return false
	}
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func envOr(e Env, name, def string) string {
	if v := e.Getenv(name); v != "" {
		return v
	}
	return def
}

func run(ctx context.Context, cfg config, e Env) int {
	unifi, err := newAPIClient("UniFi", cfg.unifiURL, cfg.unifiCA, e.Client, func(r *http.Request) {
		r.Header.Set("X-API-KEY", cfg.unifiKey)
	})
	if err != nil {
		return configError(e, err)
	}
	netbox, err := newAPIClient("NetBox", cfg.netboxURL, cfg.netboxCA, e.Client, func(r *http.Request) {
		r.Header.Set("Authorization", netboxAuthorization(cfg.netboxToken))
	})
	if err != nil {
		return configError(e, err)
	}
	devices, err := fetchUnifi[unifiDevice](ctx, unifi, cfg.apiPath, cfg.site, "device")
	if err != nil {
		return configError(e, err)
	}
	if len(devices) == 0 {
		return configError(e, errors.New("UniFi lists no device: refusing to plan against an empty network"))
	}
	clients, err := fetchUnifi[unifiClient](ctx, unifi, cfg.apiPath, cfg.site, "sta")
	if err != nil {
		return configError(e, err)
	}
	index, err := loadNetbox(ctx, netbox, cfg.tag)
	if err != nil {
		return configError(e, err)
	}

	p := buildPlan(index, cfg.tag, deriveLinks(devices, clients), cfg.prune)
	p.render(e.Stdout)
	if !cfg.apply {
		if p.writes() {
			_, _ = fmt.Fprintln(e.Stdout, "dry run: nothing written; run again with --apply")
		}
		return 0
	}
	if failed := apply(ctx, netbox, index, cfg.tag, p, e.Stdout, e.Stderr); failed > 0 {
		_, _ = fmt.Fprintf(e.Stderr, "netbox-cable-sync: %d writes failed\n", failed)
		return 1
	}
	return 0
}

func configError(e Env, err error) int {
	_, _ = fmt.Fprintf(e.Stderr, "netbox-cable-sync: %v\n", err)
	return 2
}

// netboxAuthorization picks the header scheme: v2 tokens (nbt_<id>.<secret>) are bearer tokens, v1
// tokens use "Token".
func netboxAuthorization(token string) string {
	if strings.HasPrefix(token, "nbt_") {
		return "Bearer " + token
	}
	return "Token " + token
}
