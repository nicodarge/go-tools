package lockfilecheck

import (
	"errors"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nicodarge/go-tools/internal/core"
)

// parseYAML reads a pnpm lockfile into Values without constructing anything YAML-specific: an
// anchor, an alias, an explicit tag, a second document, a duplicate key or a double-quoted or
// block scalar (outside `deprecated`) is refused, so the structure judged here cannot differ from
// the one pnpm reads by anything clever about how it is written.
//
// yaml.v3 is used for the grammar only, through its node tree; every decision about what the tree
// may hold is made here.
func parseYAML(text, which string) *value {
	dec := yaml.NewDecoder(strings.NewReader(text))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			reject("the " + which + " file is empty")
		}
		if strings.Contains(err.Error(), "unknown anchor") {
			// An alias to nothing: the event stream the Python checker reads names the alias
			// before anything resolves it.
			reject("a YAML alias")
		}
		reject("the " + which + " file is not YAML")
	}
	if len(doc.Content) != 1 {
		reject("the " + which + " file is empty")
	}
	// A refusal inside the first document comes before the second one is looked at.
	root := convertYAML(doc.Content[0], "", false)
	var second yaml.Node
	if err := dec.Decode(&second); err == nil || !errors.Is(err, io.EOF) {
		reject("more than one YAML document")
	}
	needDict(root, "the "+which+" file")
	return root
}

func yamlStyle(n *yaml.Node) uint8 {
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		return core.StyleDouble
	case n.Style&yaml.SingleQuotedStyle != 0:
		return core.StyleSingle
	case n.Style&yaml.LiteralStyle != 0:
		return core.StyleLiteral
	case n.Style&yaml.FoldedStyle != 0:
		return core.StyleFolded
	}
	return core.StylePlain
}

func checkYAMLNode(n *yaml.Node) {
	if n.Kind == yaml.AliasNode {
		reject("a YAML alias")
	}
	if n.Anchor != "" || n.Style&yaml.TaggedStyle != 0 {
		reject("a YAML anchor or tag")
	}
}

// convertYAML turns a node into a Value; key is the mapping key a scalar is the value of.
func convertYAML(n *yaml.Node, key string, hasKey bool) *value {
	checkYAMLNode(n)
	switch n.Kind {
	case yaml.ScalarNode:
		style := yamlStyle(n)
		if !(hasKey && key == "deprecated") {
			if style != core.StylePlain && style != core.StyleSingle {
				reject("a scalar that is not plain or single-quoted")
			}
			if !core.IsASCII(n.Value) {
				reject("non-ASCII text outside a deprecated message: " + q(n.Value))
			}
		}
		return &value{K: core.Str, Style: style, S: n.Value}
	case yaml.SequenceNode:
		v := &value{K: core.List}
		for _, c := range n.Content {
			v.L = append(v.L, convertYAML(c, "", false))
		}
		return v
	case yaml.MappingNode:
		m := core.NewMap()
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			checkYAMLNode(k)
			if k.Kind != yaml.ScalarNode {
				reject("a mapping key that is not a scalar")
			}
			// A key is checked as a scalar with no key above it.
			convertYAML(k, "", false)
			if m.Has(k.Value) {
				reject("duplicate key " + q(k.Value))
			}
			for _, f := range []string{"__proto__", "constructor"} {
				if k.Value == f {
					reject("the key " + q(k.Value))
				}
			}
			m.Set(k.Value, convertYAML(n.Content[i+1], k.Value, true))
		}
		return core.MapValue(m)
	}
	reject("a YAML node this does not read")
	return nil
}
