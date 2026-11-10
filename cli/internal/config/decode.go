package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

func decode(path string, data []byte) (*Config, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("configuration file %s is empty", path)
		}
		return nil, syntaxError(path, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("parse configuration %s: multiple YAML documents are not supported", path)
		}
		return nil, syntaxError(path, err)
	}

	root := document.Content[0]
	// Check the complete tree before decoding: yaml.v3 otherwise coerces some
	// scalar types and treats explicit nulls as omitted fields. Derive field
	// names and types from the Go types to avoid a second schema definition.
	if err := checkStructure(root, reflect.TypeFor[Config](), "configuration", make(map[*yaml.Node]bool)); err != nil {
		return nil, fmt.Errorf("parse configuration %s: %w", path, err)
	}
	var config Config
	if err := root.Decode(&config); err != nil {
		// Never return raw decoder diagnostics, which can contain scalar values,
		// arbitrary keys, or alias names (including truncated secret fragments).
		return nil, fmt.Errorf("parse configuration %s: values cannot be decoded into the expected types", path)
	}

	repositories := mappingValue(root, "repositories")
	if repositories != nil {
		for i := 0; i < len(repositories.Content); i += 2 {
			name := repositories.Content[i].Value
			if mappingValue(repositories.Content[i+1], "lock_wait") == nil {
				config.Repositories[name].LockWait = "60s"
			}
		}
	}
	jobs := mappingValue(root, "jobs")
	if jobs != nil {
		for i := 0; i < len(jobs.Content); i += 2 {
			job := config.Jobs[jobs.Content[i].Value]
			node := jobs.Content[i+1]
			if job.Schedule != nil {
				job.Schedule.timezoneSet = mappingValue(mappingValue(node, "schedule"), "timezone") != nil
			}
			if job.Maintenance != nil {
				maintenance := mappingValue(node, "maintenance")
				job.Maintenance.warningSet = mappingValue(maintenance, "after_backup_warning") != nil
				if !job.Maintenance.warningSet {
					job.Maintenance.AfterBackupWarning = "skip"
				}
				if job.Maintenance.Retention != nil {
					job.Maintenance.Retention.withinSet = mappingValue(mappingValue(maintenance, "retention"), "keep_within") != nil
				}
			}
		}
	}
	return &config, nil
}

var yamlLine = regexp.MustCompile(`\bline ([0-9]+)\b`)

func syntaxError(path string, err error) error {
	// Only copy a numeric line location, never the parser's diagnostic text.
	if match := yamlLine.FindStringSubmatch(err.Error()); match != nil {
		return fmt.Errorf("parse configuration %s: invalid YAML at line %s", path, match[1])
	}
	return fmt.Errorf("parse configuration %s: invalid YAML", path)
}

func structureError(node *yaml.Node, field, reason string) error {
	return fmt.Errorf("line %d: %s: %s", node.Line, field, reason)
}

func checkStructure(node *yaml.Node, kind reflect.Type, field string, active map[*yaml.Node]bool) error {
	if active[node] {
		return structureError(node, field, "recursive YAML aliases are not supported")
	}
	active[node] = true
	defer delete(active, node)
	if node.Kind == yaml.AliasNode {
		return checkStructure(node.Alias, kind, field, active)
	}
	if node.Tag == "!!null" {
		return structureError(node, field, "must not be null")
	}
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	switch kind.Kind() {
	case reflect.Struct, reflect.Map:
		if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
			return structureError(node, field, "expected a mapping")
		}
		fields := make(map[string]reflect.Type)
		if kind.Kind() == reflect.Struct {
			for i := 0; i < kind.NumField(); i++ {
				member := kind.Field(i)
				if member.IsExported() {
					fields[strings.Split(member.Tag.Get("yaml"), ",")[0]] = member.Type
				}
			}
		}
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return structureError(key, field, "expected a string key; YAML merge keys are not supported")
			}
			if seen[key.Value] {
				return structureError(key, field, "duplicate mapping key")
			}
			seen[key.Value] = true
			var childType reflect.Type
			childField := field + "[entry]"
			if kind.Kind() == reflect.Map {
				childType = kind.Elem()
			} else {
				var ok bool
				childType, ok = fields[key.Value]
				if !ok {
					return structureError(key, field, "unknown field")
				}
				// Only known field names are included in diagnostics. Unknown
				// keys and user-provided map keys may contain sensitive text.
				childField = field + "." + key.Value
			}
			if err := checkStructure(value, childType, childField, active); err != nil {
				return err
			}
			if (key.Value == "passphrase_file" || key.Value == "identity_file") && kind.Kind() == reflect.Struct && dereference(value).Value == "" {
				return structureError(value, childField, "must not be empty")
			}
		}
	case reflect.Slice:
		if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
			return structureError(node, field, "expected a sequence")
		}
		for i, value := range node.Content {
			if err := checkStructure(value, kind.Elem(), fmt.Sprintf("%s[%d]", field, i), active); err != nil {
				return err
			}
		}
	default:
		tag, label := "", ""
		switch kind.Kind() {
		case reflect.String:
			tag, label = "!!str", "a string"
		case reflect.Int:
			tag, label = "!!int", "an integer"
		case reflect.Bool:
			tag, label = "!!bool", "a boolean"
		}
		if tag == "" || node.Kind != yaml.ScalarNode || node.Tag != tag {
			return structureError(node, field, "expected "+label)
		}
	}
	return nil
}

// dereference is called only after the structure check has rejected cycles.
func dereference(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	node = dereference(node)
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return dereference(node.Content[i+1])
		}
	}
	return nil
}
