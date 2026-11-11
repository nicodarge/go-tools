package golangciguard

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

const usage = "FAIL: usage: golangci-guard config [--require LINTER,...] [DIR|FILE ...] | golangci-guard scan-go FILE..."

// Main runs the command line: `config` judges configurations, `scan-go` judges Go sources. It
// prints PASS and returns 0, or prints one `FAIL: <path>: <reason>` line per refusal and returns 1.
func Main(args []string, out io.Writer) int {
	if len(args) == 0 {
		return fail(out, usage)
	}
	switch args[0] {
	case "config":
		return runConfig(args[1:], out)
	case "scan-go":
		if len(args) < 2 {
			return fail(out, usage)
		}
		return runEach(args[1:], out, ScanGo)
	}
	return fail(out, usage)
}

func runConfig(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	require := fs.String("require", strings.Join(DefaultRequired, ","), "")
	if err := fs.Parse(args); err != nil {
		return fail(out, usage)
	}
	names := strings.Split(*require, ",")
	paths := fs.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}
	return runEach(paths, out, func(path string) error { return CheckPath(path, names) })
}

func runEach(paths []string, out io.Writer, check func(string) error) int {
	status := 0
	for _, path := range paths {
		if err := check(path); err != nil {
			status = 1
			fail(out, fmt.Sprintf("FAIL: %s: %v", path, err))
		}
	}
	if status == 0 {
		if _, err := fmt.Fprintln(out, "PASS"); err != nil {
			return 1 // a PASS nobody could read is not one
		}
	}
	return status
}

// fail prints line and returns the failing exit code; an unwritable line is not reported further.
func fail(out io.Writer, line string) int {
	_, _ = fmt.Fprintln(out, line)
	return 1
}
