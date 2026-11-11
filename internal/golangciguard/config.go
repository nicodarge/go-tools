// Package golangciguard judges a golangci-lint configuration against an allowlist and Go sources
// against the lint suppression directives.
package golangciguard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const maxDepth = 32

// DefaultRequired is the set of linters every configuration must enable; --require only adds to it.
var DefaultRequired = []string{"bodyclose", "errorlint", "gosec", "misspell", "nolintlint", "revive", "unconvert", "unparam"}

// nameRE is what a linter, formatter or analyzer name looks like.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// mapping is a YAML mapping whose keys are known to be unique plain strings.
type mapping struct {
	path  string
	order []string
	vals  map[string]*yaml.Node
}

// CheckConfig returns nil when data is a golangci-lint configuration that sits entirely on the
// allowlist and enables every linter in required, else an error naming the first refusal.
func CheckConfig(data []byte, required []string) error {
	_, err := checkConfig(data, required)
	return err
}

// checkConfig is CheckConfig, and also returns the run.build-tags of an accepted configuration.
func checkConfig(data []byte, required []string) ([]string, error) {
	for _, name := range required {
		if !nameRE.MatchString(name) {
			return nil, fmt.Errorf("required linter name %q is not a linter name", name)
		}
	}
	root, err := parseDocument(data)
	if err != nil {
		return nil, err
	}
	return validate(root, required)
}

// parseDocument reads exactly one YAML document and refuses every construct that lets what is
// written differ from what a reader of the tree sees: an alias, an anchor, an explicit tag, a merge
// key, a key that is not a plain string, a duplicate key.
func parseDocument(data []byte) (*yaml.Node, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("the file is not UTF-8 text")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the file is empty")
		}
		return nil, fmt.Errorf("the file is not YAML: %w", err)
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one YAML document")
	}
	if len(doc.Content) != 1 {
		return nil, errors.New("the file is empty")
	}
	root := doc.Content[0]
	if err := checkNode(root, 0); err != nil {
		return nil, err
	}
	return root, nil
}

func checkNode(n *yaml.Node, depth int) error {
	if depth > maxDepth {
		return errors.New("YAML nested too deeply")
	}
	if n.Kind == yaml.AliasNode {
		return errors.New("a YAML alias")
	}
	if n.Anchor != "" {
		return errors.New("a YAML anchor")
	}
	if n.Style&yaml.TaggedStyle != 0 {
		return errors.New("an explicit YAML tag")
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return nil
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if err := checkNode(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	case yaml.MappingNode:
		return checkMapping(n, depth)
	default:
		return errors.New("an unexpected YAML node")
	}
}

func checkMapping(n *yaml.Node, depth int) error {
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if err := checkNode(k, depth+1); err != nil {
			return err
		}
		if k.Kind != yaml.ScalarNode {
			return errors.New("a YAML complex key")
		}
		switch k.ShortTag() {
		case "!!merge":
			return errors.New("a YAML merge key")
		case "!!str":
		default:
			return fmt.Errorf("the key %q is not a string", k.Value)
		}
		if seen[k.Value] {
			return fmt.Errorf("the key %q appears twice", k.Value)
		}
		seen[k.Value] = true
		if err := checkNode(v, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func toMapping(n *yaml.Node, path string) (mapping, error) {
	if n.Kind != yaml.MappingNode {
		return mapping{}, fmt.Errorf("%s must be a mapping", path)
	}
	m := mapping{path: path, vals: map[string]*yaml.Node{}}
	for i := 0; i+1 < len(n.Content); i += 2 {
		m.order = append(m.order, n.Content[i].Value)
		m.vals[n.Content[i].Value] = n.Content[i+1]
	}
	return m, nil
}

// only refuses every key that is not in allowed.
func (m mapping) only(allowed ...string) error {
	for _, k := range m.order {
		found := false
		for _, a := range allowed {
			found = found || a == k
		}
		if !found {
			return fmt.Errorf("%s is not allowed", join(m.path, k))
		}
	}
	return nil
}

func (m mapping) require(key string) (*yaml.Node, error) {
	n, ok := m.vals[key]
	if !ok {
		return nil, fmt.Errorf("%s is required", join(m.path, key))
	}
	return n, nil
}

func (m mapping) requireMapping(key string) (mapping, error) {
	n, err := m.require(key)
	if err != nil {
		return mapping{}, err
	}
	return toMapping(n, join(m.path, key))
}

// scalarIs requires n to be a scalar of the given implicit tag whose text is exactly value.
func scalarIs(n *yaml.Node, path, tag, value string) error {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != tag || n.Value != value {
		return fmt.Errorf("%s must be exactly %s", path, value)
	}
	return nil
}

func (m mapping) requireScalar(key, tag, value string) error {
	n, err := m.require(key)
	if err != nil {
		return err
	}
	return scalarIs(n, join(m.path, key), tag, value)
}

func (m mapping) optionalBool(key, value string) error {
	n, ok := m.vals[key]
	if !ok {
		return nil
	}
	return scalarIs(n, join(m.path, key), "!!bool", value)
}

// names reads a sequence of distinct plain names.
func names(n *yaml.Node, path string) ([]string, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s must be a list", path)
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode || c.ShortTag() != "!!str" || !nameRE.MatchString(c.Value) {
			return nil, fmt.Errorf("%s holds an entry that is not a name", path)
		}
		if seen[c.Value] {
			return nil, fmt.Errorf("%s lists %s twice", path, c.Value)
		}
		seen[c.Value] = true
		out = append(out, c.Value)
	}
	return out, nil
}

func validate(root *yaml.Node, required []string) ([]string, error) {
	top, err := toMapping(root, "")
	if err != nil {
		return nil, errors.New("the configuration must be a mapping")
	}
	if err = top.only("version", "run", "linters", "issues", "formatters"); err != nil {
		return nil, err
	}
	if err = top.requireScalar("version", "!!str", "2"); err != nil {
		return nil, err
	}
	tags, err := validateRun(top)
	if err != nil {
		return nil, err
	}
	if err = validateLinters(top, required); err != nil {
		return nil, err
	}
	if err = validateIssues(top); err != nil {
		return nil, err
	}
	return tags, validateFormatters(top)
}

func validateRun(top mapping) ([]string, error) {
	run, err := top.requireMapping("run")
	if err != nil {
		return nil, err
	}
	if err = run.only("timeout", "tests", "build-tags"); err != nil {
		return nil, err
	}
	if err = run.requireScalar("tests", "!!bool", "true"); err != nil {
		return nil, err
	}
	if n, ok := run.vals["timeout"]; ok {
		d, perr := time.ParseDuration(n.Value)
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" || perr != nil || d <= 0 {
			return nil, errors.New("run.timeout must be a positive duration such as 5m")
		}
	}
	n, ok := run.vals["build-tags"]
	if !ok {
		return nil, nil
	}
	tags, err := names(n, "run.build-tags")
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, errors.New("run.build-tags must not be empty")
	}
	return tags, nil
}

func validateLinters(top mapping, required []string) error {
	linters, err := top.requireMapping("linters")
	if err != nil {
		return err
	}
	if err = linters.only("enable", "settings", "exclusions"); err != nil {
		return err
	}
	n, err := linters.require("enable")
	if err != nil {
		return err
	}
	enabled, err := names(n, "linters.enable")
	if err != nil {
		return err
	}
	var missing []string
	for _, r := range required {
		found := false
		for _, e := range enabled {
			found = found || e == r
		}
		if !found {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("linters.enable lacks %s", strings.Join(missing, ", "))
	}
	exclusions, err := linters.requireMapping("exclusions")
	if err != nil {
		return err
	}
	if err = exclusions.only("generated"); err != nil {
		return err
	}
	if err = exclusions.requireScalar("generated", "!!str", "disable"); err != nil {
		return err
	}
	if s, ok := linters.vals["settings"]; ok {
		return validateSettings(s, enabled)
	}
	return requireGosecSettings(enabled, false)
}

func requireGosecSettings(enabled []string, present bool) error {
	for _, e := range enabled {
		if e == "gosec" && !present {
			return errors.New("linters.settings.gosec is required when gosec is enabled")
		}
	}
	return nil
}

func validateSettings(n *yaml.Node, enabled []string) error {
	settings, err := toMapping(n, "linters.settings")
	if err != nil {
		return err
	}
	if err = settings.only("gosec", "nolintlint", "govet"); err != nil {
		return err
	}
	if g, ok := settings.vals["gosec"]; ok {
		if err = validateGosec(g); err != nil {
			return err
		}
	}
	if err = requireGosecSettings(enabled, settings.vals["gosec"] != nil); err != nil {
		return err
	}
	if l, ok := settings.vals["nolintlint"]; ok {
		if err = validateNolintlint(l); err != nil {
			return err
		}
	}
	if v, ok := settings.vals["govet"]; ok {
		return validateGovet(v)
	}
	return nil
}

// validateGosec accepts config.global set to the one entry that makes gosec ignore its suppression
// comments, and nothing else: its other settings (includes, excludes, severity, confidence, rule
// entries) can only narrow what it reports.
func validateGosec(n *yaml.Node) error {
	gosec, err := toMapping(n, "linters.settings.gosec")
	if err != nil {
		return err
	}
	if err = gosec.only("config"); err != nil {
		return err
	}
	config, err := gosec.requireMapping("config")
	if err != nil {
		return err
	}
	if err = config.only("global"); err != nil {
		return err
	}
	global, err := config.requireMapping("global")
	if err != nil {
		return err
	}
	if err = global.only("nosec"); err != nil {
		return err
	}
	return global.requireScalar("nosec", "!!bool", "true")
}

func validateNolintlint(n *yaml.Node) error {
	nl, err := toMapping(n, "linters.settings.nolintlint")
	if err != nil {
		return err
	}
	if err = nl.only("require-explanation", "require-specific", "allow-unused"); err != nil {
		return err
	}
	if err = nl.optionalBool("require-explanation", "true"); err != nil {
		return err
	}
	if err = nl.optionalBool("require-specific", "true"); err != nil {
		return err
	}
	return nl.optionalBool("allow-unused", "false")
}

// validateGovet accepts the analyzers it adds and enable-all: true; disable and disable-all are refused.
func validateGovet(n *yaml.Node) error {
	govet, err := toMapping(n, "linters.settings.govet")
	if err != nil {
		return err
	}
	if err = govet.only("enable", "enable-all"); err != nil {
		return err
	}
	if e, ok := govet.vals["enable"]; ok {
		if _, err = names(e, "linters.settings.govet.enable"); err != nil {
			return err
		}
	}
	return govet.optionalBool("enable-all", "true")
}

func validateIssues(top mapping) error {
	issues, err := top.requireMapping("issues")
	if err != nil {
		return err
	}
	if err = issues.only("max-issues-per-linter", "max-same-issues"); err != nil {
		return err
	}
	if err = issues.requireScalar("max-issues-per-linter", "!!int", "0"); err != nil {
		return err
	}
	return issues.requireScalar("max-same-issues", "!!int", "0")
}

func validateFormatters(top mapping) error {
	n, ok := top.vals["formatters"]
	if !ok {
		return nil
	}
	formatters, err := toMapping(n, "formatters")
	if err != nil {
		return err
	}
	if err = formatters.only("enable"); err != nil {
		return err
	}
	if e, ok := formatters.vals["enable"]; ok {
		_, err = names(e, "formatters.enable")
	}
	return err
}
