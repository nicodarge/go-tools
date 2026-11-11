package golangciguard

import (
	"strings"
	"testing"
)

const validConfig = `---
version: "2"

run:
  tests: true

linters:
  enable:
    - bodyclose
    - errorlint
    - gosec
    - misspell
    - nolintlint
    - revive
    - unconvert
    - unparam
  settings:
    govet:
      enable:
        - shadow
    gosec:
      config:
        global:
          nosec: true
  exclusions:
    generated: disable

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
`

// edit applies old -> new pairs to validConfig; an old that is not in it fails the test, so a
// mutation that no longer matches cannot pass as a refusal that never happened.
func edit(t *testing.T, pairs ...string) string {
	t.Helper()
	text := validConfig
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(text, pairs[i]) {
			t.Fatalf("the base configuration has no %q", pairs[i])
		}
		text = strings.Replace(text, pairs[i], pairs[i+1], 1)
	}
	return text
}

func TestBaseConfigurationIsAccepted(t *testing.T) {
	if err := CheckConfig([]byte(validConfig), DefaultRequired); err != nil {
		t.Fatalf("the base configuration is refused: %v", err)
	}
}

func TestAcceptedVariations(t *testing.T) {
	for label, text := range map[string]string{
		"no document marker":      strings.TrimPrefix(validConfig, "---\n"),
		"a timeout":               edit(t, "  tests: true\n", "  tests: true\n  timeout: 5m\n"),
		"a quoted timeout":        edit(t, "  tests: true\n", "  tests: true\n  timeout: \"90s\"\n"),
		"extra linters":           edit(t, "    - bodyclose\n", "    - bodyclose\n    - wsl_v5\n    - go-checksumtype\n"),
		"a formatter":             validConfig + "\nformatters:\n  enable:\n    - gofmt\n    - goimports\n",
		"no settings":             edit(t, "  settings:\n    govet:\n      enable:\n        - shadow\n    gosec:\n      config:\n        global:\n          nosec: true\n", "", "linters:\n", "linters:\n  settings:\n    gosec:\n      config:\n        global:\n          nosec: true\n"),
		"strict nolintlint":       edit(t, "  exclusions:", "    nolintlint:\n      require-explanation: true\n      require-specific: true\n      allow-unused: false\n  exclusions:"),
		"govet enable-all":        edit(t, "      enable:\n        - shadow\n", "      enable-all: true\n"),
		"flow lists and comments": edit(t, "  enable:\n    - bodyclose\n    - errorlint\n    - gosec\n    - misspell\n    - nolintlint\n    - revive\n    - unconvert\n    - unparam\n", "  enable: [bodyclose, errorlint, gosec, misspell, nolintlint, revive, unconvert, unparam] # all of them\n"),
	} {
		if err := CheckConfig([]byte(text), DefaultRequired); err != nil {
			t.Errorf("%s is refused: %v", label, err)
		}
	}
}

func TestRefusedConfigurations(t *testing.T) {
	for label, tc := range map[string]struct{ text, want string }{
		// The bypasses the regular-expression hooks let through.
		"gosec severity high":            {edit(t, "    gosec:\n      config:", "    gosec:\n      severity: high\n      config:"), "linters.settings.gosec.severity is not allowed"},
		"gosec confidence high":          {edit(t, "    gosec:\n      config:", "    gosec:\n      confidence: high\n      config:"), "linters.settings.gosec.confidence is not allowed"},
		"gosec includes":                 {edit(t, "    gosec:\n      config:", "    gosec:\n      includes: [G402]\n      config:"), "linters.settings.gosec.includes is not allowed"},
		"gosec excludes":                 {edit(t, "    gosec:\n      config:", "    gosec:\n      excludes: [G304]\n      config:"), "linters.settings.gosec.excludes is not allowed"},
		"gosec without its config":       {edit(t, "      config:\n        global:\n          nosec: true\n", "      {}\n"), "linters.settings.gosec.config is required"},
		"gosec nosec false":              {edit(t, "nosec: true", "nosec: false"), "linters.settings.gosec.config.global.nosec must be exactly true"},
		"gosec global extra":             {edit(t, "          nosec: true\n", "          nosec: true\n          audit: false\n"), "linters.settings.gosec.config.global.audit is not allowed"},
		"gosec config extra":             {edit(t, "        global:\n", "        G101:\n          pattern: x\n        global:\n"), "linters.settings.gosec.config.G101 is not allowed"},
		"gosec missing while enabled":    {edit(t, "    gosec:\n      config:\n        global:\n          nosec: true\n", ""), "linters.settings.gosec is required when gosec is enabled"},
		"no settings while gosec used":   {edit(t, "  settings:\n    govet:\n      enable:\n        - shadow\n    gosec:\n      config:\n        global:\n          nosec: true\n", ""), "linters.settings.gosec is required when gosec is enabled"},
		"max issues per linter 1":        {edit(t, "max-issues-per-linter: 0", "max-issues-per-linter: 1"), "issues.max-issues-per-linter must be exactly 0"},
		"max issues per linter 50":       {edit(t, "max-issues-per-linter: 0", "max-issues-per-linter: 50"), "issues.max-issues-per-linter must be exactly 0"},
		"max issues per linter missing":  {edit(t, "  max-issues-per-linter: 0\n", ""), "issues.max-issues-per-linter is required"},
		"max same issues 3":              {edit(t, "max-same-issues: 0", "max-same-issues: 3"), "issues.max-same-issues must be exactly 0"},
		"max same issues missing":        {edit(t, "  max-same-issues: 0\n", ""), "issues.max-same-issues is required"},
		"max same issues hex zero":       {edit(t, "max-same-issues: 0", "max-same-issues: 0x0"), "issues.max-same-issues must be exactly 0"},
		"max same issues a string":       {edit(t, "max-same-issues: 0", "max-same-issues: \"0\""), "issues.max-same-issues must be exactly 0"},
		"issues missing":                 {edit(t, "\nissues:\n  max-issues-per-linter: 0\n  max-same-issues: 0\n", ""), "issues is required"},
		"issues extra key":               {edit(t, "  max-same-issues: 0\n", "  max-same-issues: 0\n  new: true\n"), "issues.new is not allowed"},
		"issues exclude rules":           {edit(t, "  max-same-issues: 0\n", "  max-same-issues: 0\n  exclude-rules: []\n"), "issues.exclude-rules is not allowed"},
		"linter dropped":                 {edit(t, "    - unparam\n", ""), "linters.enable lacks unparam"},
		"several linters dropped":        {edit(t, "    - unparam\n", "", "    - revive\n", ""), "linters.enable lacks revive, unparam"},
		"gosec dropped":                  {edit(t, "    - gosec\n", ""), "linters.enable lacks gosec"},
		"no enable":                      {edit(t, "  enable:\n    - bodyclose\n    - errorlint\n    - gosec\n    - misspell\n    - nolintlint\n    - revive\n    - unconvert\n    - unparam\n", ""), "linters.enable is required"},
		"enable twice":                   {edit(t, "    - unparam\n", "    - unparam\n    - unparam\n"), "linters.enable lists unparam twice"},
		"enable a mapping":               {edit(t, "    - unparam\n", "    - unparam: {}\n"), "linters.enable holds an entry that is not a name"},
		"enable an odd name":             {edit(t, "    - unparam\n", "    - Un Param\n"), "linters.enable holds an entry that is not a name"},
		"enable not a list":              {edit(t, "  enable:\n    - bodyclose\n    - errorlint\n    - gosec\n    - misspell\n    - nolintlint\n    - revive\n    - unconvert\n    - unparam\n", "  enable: all\n"), "linters.enable must be a list"},
		"staticcheck checks all off":     {edit(t, "  exclusions:", "    staticcheck:\n      checks: [\"-all\"]\n  exclusions:"), "linters.settings.staticcheck is not allowed"},
		"revive rules":                   {edit(t, "  exclusions:", "    revive:\n      rules: []\n  exclusions:"), "linters.settings.revive is not allowed"},
		"nolintlint allow unused":        {edit(t, "  exclusions:", "    nolintlint:\n      allow-unused: true\n  exclusions:"), "linters.settings.nolintlint.allow-unused must be exactly false"},
		"nolintlint explanation off":     {edit(t, "  exclusions:", "    nolintlint:\n      require-explanation: false\n  exclusions:"), "linters.settings.nolintlint.require-explanation must be exactly true"},
		"nolintlint specific off":        {edit(t, "  exclusions:", "    nolintlint:\n      require-specific: false\n  exclusions:"), "linters.settings.nolintlint.require-specific must be exactly true"},
		"nolintlint allow no explain":    {edit(t, "  exclusions:", "    nolintlint:\n      allow-no-explanation: [gosec]\n  exclusions:"), "linters.settings.nolintlint.allow-no-explanation is not allowed"},
		"govet disable":                  {edit(t, "      enable:\n        - shadow\n", "      disable:\n        - printf\n"), "linters.settings.govet.disable is not allowed"},
		"govet disable-all":              {edit(t, "      enable:\n        - shadow\n", "      disable-all: true\n"), "linters.settings.govet.disable-all is not allowed"},
		"govet enable-all false":         {edit(t, "      enable:\n        - shadow\n", "      enable-all: false\n"), "linters.settings.govet.enable-all must be exactly true"},
		"settings not a mapping":         {edit(t, "  settings:\n    govet:\n      enable:\n        - shadow\n    gosec:\n      config:\n        global:\n          nosec: true\n", "  settings: []\n"), "linters.settings must be a mapping"},
		"settings null":                  {edit(t, "  settings:\n    govet:\n      enable:\n        - shadow\n    gosec:\n      config:\n        global:\n          nosec: true\n", "  settings:\n"), "linters.settings must be a mapping"},
		"run build tags":                 {edit(t, "  tests: true\n", "  tests: true\n  build-tags: [ignore]\n"), "run.build-tags is not allowed"},
		"run skip dirs":                  {edit(t, "  tests: true\n", "  tests: true\n  skip-dirs: [internal]\n"), "run.skip-dirs is not allowed"},
		"run tests false":                {edit(t, "tests: true", "tests: false"), "run.tests must be exactly true"},
		"run tests yes":                  {edit(t, "tests: true", "tests: yes"), "run.tests must be exactly true"},
		"run tests capitalized":          {edit(t, "tests: true", "tests: True"), "run.tests must be exactly true"},
		"run tests missing":              {edit(t, "  tests: true\n", "  timeout: 5m\n"), "run.tests is required"},
		"run missing":                    {edit(t, "run:\n  tests: true\n\n", ""), "run is required"},
		"run timeout not a duration":     {edit(t, "  tests: true\n", "  tests: true\n  timeout: soon\n"), "run.timeout must be a positive duration"},
		"run timeout zero":               {edit(t, "  tests: true\n", "  tests: true\n  timeout: 0s\n"), "run.timeout must be a positive duration"},
		"run timeout negative":           {edit(t, "  tests: true\n", "  tests: true\n  timeout: -5m\n"), "run.timeout must be a positive duration"},
		"version 1":                      {edit(t, `version: "2"`, `version: "1"`), "version must be exactly 2"},
		"version a number":               {edit(t, `version: "2"`, `version: 2`), "version must be exactly 2"},
		"version 2.0":                    {edit(t, `version: "2"`, `version: "2.0"`), "version must be exactly 2"},
		"version missing":                {edit(t, "version: \"2\"\n", ""), "version is required"},
		"default key":                    {edit(t, "linters:\n", "linters:\n  default: none\n"), "linters.default is not allowed"},
		"default standard":               {edit(t, "linters:\n", "linters:\n  default: standard\n"), "linters.default is not allowed"},
		"default as an explicit key":     {edit(t, "linters:\n", "linters:\n  ? default\n  : none\n"), "linters.default is not allowed"},
		"disable":                        {edit(t, "linters:\n", "linters:\n  disable: [errcheck]\n"), "linters.disable is not allowed"},
		"exclusions presets":             {edit(t, "    generated: disable\n", "    generated: disable\n    presets: [comments]\n"), "linters.exclusions.presets is not allowed"},
		"exclusions rules":               {edit(t, "    generated: disable\n", "    generated: disable\n    rules: []\n"), "linters.exclusions.rules is not allowed"},
		"exclusions paths":               {edit(t, "    generated: disable\n", "    generated: disable\n    paths: [x]\n"), "linters.exclusions.paths is not allowed"},
		"exclusions generated lax":       {edit(t, "generated: disable", "generated: lax"), "linters.exclusions.generated must be exactly disable"},
		"exclusions generated strict":    {edit(t, "generated: disable", "generated: strict"), "linters.exclusions.generated must be exactly disable"},
		"exclusions generated quoted":    {edit(t, "generated: disable", "generated: \"disable \""), "linters.exclusions.generated must be exactly disable"},
		"exclusions generated missing":   {edit(t, "    generated: disable\n", "    {}\n"), "linters.exclusions.generated is required"},
		"exclusions missing":             {edit(t, "  exclusions:\n    generated: disable\n", ""), "linters.exclusions is required"},
		"generated under issues":         {edit(t, "  exclusions:\n    generated: disable\n", "", "  max-same-issues: 0\n", "  max-same-issues: 0\n  generated: disable\n"), "linters.exclusions is required"},
		"generated under settings":       {edit(t, "  exclusions:\n    generated: disable\n", "    generated: disable\n"), "linters.exclusions is required"},
		"generated at the top":           {edit(t, "  exclusions:\n    generated: disable\n", "", "\nissues:", "generated: disable\n\nissues:"), "generated is not allowed"},
		"exclusions at the top":          {edit(t, "  exclusions:\n    generated: disable\n", "", "\nissues:", "exclusions:\n  generated: disable\n\nissues:"), "exclusions is not allowed"},
		"unknown top-level key":          {validConfig + "output:\n  formats: []\n", "output is not allowed"},
		"severity top-level key":         {validConfig + "severity:\n  default: info\n", "severity is not allowed"},
		"upper-case top-level key":       {strings.Replace(validConfig, "linters:", "Linters:", 1), "Linters is not allowed"},
		"upper-case nested key":          {strings.Replace(validConfig, "  enable:\n    - bodyclose", "  Enable:\n    - bodyclose", 1), "linters.Enable is not allowed"},
		"formatters exclusions":          {validConfig + "formatters:\n  enable: [gofmt]\n  exclusions:\n    paths: [x]\n", "formatters.exclusions is not allowed"},
		"formatters settings":            {validConfig + "formatters:\n  settings:\n    gofmt:\n      simplify: false\n", "formatters.settings is not allowed"},
		"formatters odd name":            {validConfig + "formatters:\n  enable: [\"Go Fmt\"]\n", "formatters.enable holds an entry that is not a name"},
		"formatters null":                {validConfig + "formatters:\n", "formatters must be a mapping"},
		"run null":                       {edit(t, "run:\n  tests: true\n", "run:\n"), "run must be a mapping"},
		"required key with a null value": {edit(t, "  max-same-issues: 0\n", "  max-same-issues:\n"), "issues.max-same-issues must be exactly 0"},

		// YAML constructs that hide what a reader of the file sees.
		"complex key sequence":    {edit(t, "linters:\n", "linters:\n  ? [default]\n  : none\n"), "a YAML complex key"},
		"complex key mapping":     {edit(t, "linters:\n", "linters:\n  ? {a: b}\n  : none\n"), "a YAML complex key"},
		"anchor":                  {edit(t, "    - bodyclose\n", "    - &bc bodyclose\n"), "a YAML anchor"},
		"anchor on a mapping":     {edit(t, "  exclusions:\n", "  exclusions: &x\n"), "a YAML anchor"},
		"alias":                   {edit(t, "    - bodyclose\n", "    - &bc bodyclose\n    - *bc\n"), "a YAML anchor"},
		"alias only":              {edit(t, "  max-same-issues: 0\n", "  max-same-issues: *missing\n"), "unknown anchor"},
		"merge key":               {edit(t, "linters:\n", "linters:\n  <<: {default: none}\n"), "a YAML merge key"},
		"merge key at the top":    {validConfig + "<<: {output: {}}\n", "a YAML merge key"},
		"explicit tag":            {edit(t, "max-same-issues: 0", "max-same-issues: !!int 0"), "an explicit YAML tag"},
		"explicit tag on a key":   {edit(t, "  max-same-issues: 0\n", "  !!str max-same-issues: 0\n"), "an explicit YAML tag"},
		"duplicate key":           {edit(t, "  max-same-issues: 0\n", "  max-same-issues: 0\n  max-same-issues: 5\n"), `the key "max-same-issues" appears twice`},
		"duplicate top-level key": {validConfig + "issues:\n  max-issues-per-linter: 50\n", `the key "issues" appears twice`},
		"duplicate key reversed":  {edit(t, "issues:\n", "issues:\n  max-same-issues: 7\n"), `the key "max-same-issues" appears twice`},
		"second document":         {validConfig + "---\nversion: \"1\"\n", "more than one YAML document"},
		"empty second document":   {validConfig + "---\n", "more than one YAML document"},
		"document end then more":  {validConfig + "...\n---\nlinters: {}\n", "more than one YAML document"},
		"non-string key":          {validConfig + "1: x\n", `the key "1" is not a string`},
		"null key":                {validConfig + "~: x\n", `is not a string`},
		"bool key":                {validConfig + "true: x\n", `the key "true" is not a string`},
		"empty file":              {"", "the file is empty"},
		"comments only":           {"# nothing\n", "the file is empty"},
		"null document":           {"~\n", "the configuration must be a mapping"},
		"a list document":         {"- a\n- b\n", "the configuration must be a mapping"},
		"a scalar document":       {"version\n", "the configuration must be a mapping"},
		"not YAML":                {"a: [b\n", "the file is not YAML"},
		"invalid UTF-8":           {validConfig + "# \xff\n", "the file is not UTF-8 text"},
		"a NUL byte":              {validConfig + "# \x00\n", "the file is not UTF-8 text"},
		"nested too deeply":       {strings.Repeat("[", 40) + strings.Repeat("]", 40), "nested too deeply"},
	} {
		err := CheckConfig([]byte(tc.text), DefaultRequired)
		switch {
		case err == nil:
			t.Errorf("%s: accepted, want %q", label, tc.want)
		case !strings.Contains(err.Error(), tc.want):
			t.Errorf("%s: refused with %q, want %q", label, err, tc.want)
		}
	}
}

func TestRequiredSet(t *testing.T) {
	if err := CheckConfig([]byte(validConfig), []string{"gosec", "revive"}); err != nil {
		t.Errorf("a subset of the enabled linters is refused: %v", err)
	}
	withoutGosec := edit(t, "    - gosec\n", "", "    gosec:\n      config:\n        global:\n          nosec: true\n", "")
	if err := CheckConfig([]byte(withoutGosec), []string{"revive"}); err != nil {
		t.Errorf("a configuration without gosec and without gosec settings is refused: %v", err)
	}
	if err := CheckConfig([]byte(validConfig), nil); err != nil {
		t.Errorf("an empty required set is refused: %v", err)
	}
	if err := CheckConfig([]byte(validConfig), []string{"gosec", "wsl_v5"}); err == nil || !strings.Contains(err.Error(), "linters.enable lacks wsl_v5") {
		t.Errorf("a required linter that is not enabled: %v", err)
	}
	for _, bad := range []string{"", "Gosec", "gosec revive", "gosec,", "-all"} {
		if err := CheckConfig([]byte(validConfig), []string{bad}); err == nil || !strings.Contains(err.Error(), "is not a linter name") {
			t.Errorf("the required name %q: %v", bad, err)
		}
	}
}
