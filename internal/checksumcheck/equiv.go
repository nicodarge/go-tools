//go:build equiv

package checksumcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nicodarge/go-tools/internal/core"
)

// The equivalence build only: the entry points the Python test suite calls in-process, as
// subcommands, and the digests it injects in place of what upstream publishes. Never part of the
// shipped build.

func init() {
	if path := os.Getenv("EQUIV_DIGESTS"); path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			err = json.Unmarshal(data, &equivDigests)
		}
		if err != nil {
			panic(err)
		}
		if equivDigests == nil {
			equivDigests = map[string]string{}
		}
	}
}

func namesFrom(file string) []asset {
	data, err := os.ReadFile(file)
	if err != nil {
		panic(err)
	}
	var pairs [][2]string
	if err := json.Unmarshal(data, &pairs); err != nil {
		panic(err)
	}
	var names []asset
	for _, p := range pairs {
		names = append(names, asset{p[0], p[1]})
	}
	return names
}

// EquivCommand runs one of the equivalence subcommands; handled is false for any other name.
func EquivCommand(name string, args []string, out io.Writer) (code int, handled bool) {
	var result string
	fail := ""
	switch name {
	case "parse-tools":
		fail = core.Guard(func() {
			text, _ := os.ReadFile(args[0])
			_, order := ParseTools(string(text))
			result = "OK " + strings.Join(order, " ")
		})
	case "checksum-file", "release":
		fail = core.Guard(func() {
			data, _ := os.ReadFile(args[0])
			var got map[string]string
			if name == "release" {
				got = fromRelease(data, namesFrom(args[1]), args[2])
			} else {
				got = fromChecksumFile(data, namesFrom(args[1]), args[2])
			}
			enc, _ := json.Marshal(got)
			result = string(enc)
		})
	case "redirect":
		target, _ := url.Parse(args[1])
		result = "refuse"
		if redirectAllowed(args[0], target) {
			result = "follow"
		}
	case "urlopen-host":
		fail = core.Guard(func() {
			urlopen(nil, args[0], time.Second, nil)
			result = "allowed"
		})
	case "fetch-digests":
		fail = core.Guard(func() {
			text, _ := os.ReadFile(args[0])
			tools, _ := ParseTools(string(text))
			var ubuntu *string
			if args[3] != "-" {
				ubuntu = &args[3]
			}
			var fixtures urlFixtures
			data, _ := os.ReadFile(args[5])
			if err := json.Unmarshal(data, &fixtures); err != nil {
				panic(err)
			}
			got := fetchDigests(fixtures, tools[args[1]], args[1], args[2], ubuntu, strings.Split(args[4], ","), newBudget())
			enc, _ := json.Marshal(got)
			result = string(enc)
		})
	default:
		return 0, false
	}
	if fail != "" {
		result = fail
	}
	fmt.Fprintln(out, result)
	return 0, true
}
