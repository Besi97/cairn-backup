package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// DisplayText escapes terminal controls in configuration-derived text.
func DisplayText(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			quoted := strconv.QuoteRune(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// SanitizedYAML returns normalized, unresolved YAML from the loaded document,
// without comments, anchors or inline credentials. It never reads a file.
// Fields select mapping keys or zero-based sequence indices.
func (config *Config) SanitizedYAML(fields ...string) string {
	node := config.display
	for _, field := range fields {
		if node == nil {
			return ""
		}
		var next *yaml.Node
		switch node.Kind {
		case yaml.MappingNode:
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == field {
					next = node.Content[i+1]
					break
				}
			}
		case yaml.SequenceNode:
			if index, err := strconv.Atoi(field); err == nil && index >= 0 && index < len(node.Content) {
				next = node.Content[index]
			}
		}
		node = next
	}
	if node == nil {
		return ""
	}
	var out strings.Builder
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(displayKeys(node)); err != nil {
		return ""
	}
	return out.String()
}

func displaySnapshot(doc *yaml.Node, value any) *yaml.Node {
	secrets := map[*yaml.Node]bool{}
	repositories := value.(map[string]any)["repositories"].(map[string]any)
	for name, entry := range repositories {
		if _, present := entry.(map[string]any)["passphrase"]; present {
			secrets[at(doc, []string{"repositories", name, "passphrase"})] = true
		}
	}
	return displayNode(redactAliases(doc, value, secrets), nil)
}

func redactAliases(node *yaml.Node, value any, secrets map[*yaml.Node]bool) any {
	for node.Kind == yaml.DocumentNode || node.Kind == yaml.AliasNode {
		if node.Kind == yaml.DocumentNode {
			node = node.Content[0]
		} else {
			node = node.Alias
		}
	}
	if secrets[node] {
		return "[redacted]"
	}
	switch value := value.(type) {
	case map[string]any:
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i].Value
			value[key] = redactAliases(node.Content[i+1], value[key], secrets)
		}
	case []any:
		for i, child := range node.Content {
			value[i] = redactAliases(child, value[i], secrets)
		}
	}
	return value
}

func displayNode(value any, fields []string) *yaml.Node {
	node := &yaml.Node{}
	switch value := value.(type) {
	case map[string]any:
		node.Kind, node.Tag = yaml.MappingNode, "!!map"
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := append(append([]string{}, fields...), key)
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, displayNode(value[key], next))
		}
	case []any:
		node.Kind, node.Tag = yaml.SequenceNode, "!!seq"
		for i, entry := range value {
			node.Content = append(node.Content, displayNode(entry, append(fields, strconv.Itoa(i))))
		}
	case string:
		if len(fields) == 3 && fields[0] == "repositories" {
			switch fields[2] {
			case "passphrase":
				value = "[redacted]"
			case "location":
				value = displayLocation(value)
			}
		}
		node.Kind, node.Tag, node.Value = yaml.ScalarNode, "!!str", DisplayText(value)
	case json.Number:
		node.Kind, node.Tag, node.Value = yaml.ScalarNode, "!!int", value.String()
		if strings.ContainsAny(value.String(), ".eE") {
			node.Tag = "!!float"
		}
	case bool:
		node.Kind, node.Tag, node.Value = yaml.ScalarNode, "!!bool", strconv.FormatBool(value)
	case nil:
		node.Kind, node.Tag, node.Value = yaml.ScalarNode, "!!null", "null"
	default:
		panic(fmt.Sprintf("unexpected validated configuration type %T", value))
	}
	return node
}

func displayKeys(node *yaml.Node) *yaml.Node {
	copy := *node
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = displayKeys(child)
		if node.Kind == yaml.MappingNode && i%2 == 0 {
			copy.Content[i].Value = DisplayText(child.Value)
		}
	}
	return &copy
}

func displayLocation(location string) string {
	// Do not parse Borg placeholders as URLs. Passwords can only be part of
	// userinfo before the authority's final @, never the repository path.
	if !strings.HasPrefix(location, "ssh://") {
		return location
	}
	authority, path, found := strings.Cut(strings.TrimPrefix(location, "ssh://"), "/")
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		if colon := strings.IndexByte(authority[:at], ':'); colon >= 0 {
			authority = authority[:colon+1] + "[redacted]" + authority[at:]
		}
	}
	if found {
		return "ssh://" + authority + "/" + path
	}
	return "ssh://" + authority
}
