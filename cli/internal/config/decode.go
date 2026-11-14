package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Node conversion is bounded even for recursive or exponentially shared aliases.
const maxNodes = 100000
const maxDepth = 100

var yamlLine = regexp.MustCompile(`(?:^|\n)yaml: line ([0-9]+):`)
var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func decode(path string, data []byte) (*yaml.Node, any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, nil, syntaxError(path, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, nil, syntaxError(path, err)
		}
		return nil, nil, diagnostic(path, &extra, nil, "multiple YAML documents are not allowed")
	}
	remaining := maxNodes
	value, err := nodeValue(path, &doc, nil, 0, &remaining)
	return &doc, value, err
}

func syntaxError(path string, err error) error {
	// Parser errors may contain the offending scalar, alias name, or secret.
	// Extract only the numeric line marker from the parser's fixed prefix.
	if match := yamlLine.FindStringSubmatch(err.Error()); match != nil {
		return fmt.Errorf("%s:%s: invalid YAML syntax", path, match[1])
	}
	return fmt.Errorf("%s: invalid YAML syntax", path)
}

func diagnostic(path string, node *yaml.Node, fields []string, reason string) error {
	location := path
	if node != nil && node.Line > 0 {
		location += fmt.Sprintf(":%d:%d", node.Line, node.Column)
	}
	field := "/"
	if len(fields) > 0 {
		escaped := make([]string, len(fields))
		for i, entry := range fields {
			escaped[i] = strings.ReplaceAll(strings.ReplaceAll(entry, "~", "~0"), "/", "~1")
		}
		field += strings.Join(escaped, "/")
	}
	return fmt.Errorf("%s: %s: %s", location, field, reason)
}

func nodeValue(path string, node *yaml.Node, fields []string, depth int, remaining *int) (any, error) {
	(*remaining)--
	fail := func(reason string) (any, error) {
		return nil, diagnostic(path, node, safeFields(fields), reason)
	}
	if depth > maxDepth || *remaining < 0 {
		return fail("YAML nesting or alias expansion limit exceeded")
	}
	child := func(n *yaml.Node, key string) (any, error) {
		next := append(append([]string{}, fields...), key)
		return nodeValue(path, n, next, depth+1, remaining)
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return fail("expected one configuration object")
		}
		return nodeValue(path, node.Content[0], fields, depth+1, remaining)
	case yaml.AliasNode:
		return nodeValue(path, node.Alias, fields, depth+1, remaining)
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return fail("unsupported YAML mapping tag")
		}
		result := make(map[string]any)
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fail("mapping keys must be strings; YAML merge keys are not supported")
			}
			if _, exists := result[key.Value]; exists {
				return fail("duplicate mapping key")
			}
			entry, err := child(value, key.Value)
			if err != nil {
				return nil, err
			}
			result[key.Value] = entry
		}
		return result, nil
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return fail("unsupported YAML sequence tag")
		}
		result := make([]any, len(node.Content))
		for i, value := range node.Content {
			entry, err := child(value, strconv.Itoa(i))
			if err != nil {
				return nil, err
			}
			result[i] = entry
		}
		return result, nil
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str":
			return node.Value, nil
		case "!!null":
			return nil, nil
		case "!!bool":
			return strings.EqualFold(node.Value, "true"), nil
		case "!!int":
			number, ok := new(big.Int).SetString(strings.ReplaceAll(node.Value, "_", ""), 0)
			if ok {
				return json.Number(number.String()), nil
			}
		case "!!float":
			literal := strings.ReplaceAll(node.Value, "_", "")
			number, err := strconv.ParseFloat(literal, 64)
			if err == nil && !math.IsNaN(number) && !math.IsInf(number, 0) {
				// Preserve exact integer-valued numbers without float64 rounding.
				exact, ok := new(big.Rat).SetString(literal)
				if ok && exact.IsInt() {
					return json.Number(exact.Num().String()), nil
				}
				// Normalize YAML's decimal spellings to JSON syntax without
				// rounding nonintegers into integers (e.g. 1.000000000000001).
				literal = strings.TrimPrefix(literal, "+")
				sign := ""
				if strings.HasPrefix(literal, "-") {
					sign, literal = "-", literal[1:]
				}
				mantissa, exponent := literal, ""
				if i := strings.IndexAny(literal, "eE"); i >= 0 {
					mantissa, exponent = literal[:i], literal[i:]
				}
				mantissa = strings.TrimLeft(mantissa, "0")
				if mantissa == "" || strings.HasPrefix(mantissa, ".") {
					mantissa = "0" + mantissa
				}
				if strings.HasSuffix(mantissa, ".") {
					mantissa += "0"
				}
				literal = sign + mantissa + exponent
				if jsonNumber.MatchString(literal) {
					return json.Number(literal), nil
				}
			}
		}
	}
	return fail("unsupported or non-JSON YAML value")
}

// Before schema validation, arbitrary keys can occur anywhere, even within a
// malformed passphrase. Only expose known fields and dynamic names in their
// contracted positions; do not include unvalidated keys in diagnostics.
func safeFields(fields []string) []string {
	var result []string
	for i, field := range fields {
		if i == 1 && (fields[0] == "repositories" || fields[0] == "workflows") {
			result = append(result, field)
			continue
		}
		if _, err := strconv.Atoi(field); err == nil || knownFields[field] {
			result = append(result, field)
			continue
		}
		break
	}
	return result
}

var knownFields = map[string]bool{
	"version": true, "repositories": true, "workflows": true,
	"location": true, "passphrase": true, "passphrase_file": true, "identity_file": true,
	"cron": true, "stages": true, "tasks": true, "type": true, "repository": true,
	"archive_prefix": true, "archive_name": true, "source_root": true, "sources": true,
	"exclude": true, "lock_wait": true, "keep_within": true, "keep_secondly": true,
	"keep_minutely": true, "keep_hourly": true, "keep_daily": true, "keep_weekly": true,
	"keep_monthly": true, "keep_yearly": true,
}

func at(doc *yaml.Node, fields []string) *yaml.Node {
	node := doc
	for _, field := range fields {
		for node.Kind == yaml.DocumentNode || node.Kind == yaml.AliasNode {
			if node.Kind == yaml.DocumentNode {
				node = node.Content[0]
			} else {
				node = node.Alias
			}
		}
		switch node.Kind {
		case yaml.MappingNode:
			var found *yaml.Node
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == field {
					found = node.Content[i+1]
					break
				}
			}
			if found == nil {
				return node
			}
			node = found
		case yaml.SequenceNode:
			index, err := strconv.Atoi(field)
			if err != nil || index < 0 || index >= len(node.Content) {
				return node
			}
			node = node.Content[index]
		default:
			return node
		}
	}
	for node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}
