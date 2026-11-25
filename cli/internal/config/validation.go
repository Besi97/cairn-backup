package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	published "github.com/Besi97/cairn-backup/cli/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"go.yaml.in/yaml/v4"
)

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(published.V1))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://raw.githubusercontent.com/Besi97/cairn-backup/main/cli/schema/config-v1.schema.json"
	if err := compiler.AddResource(location, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
})

func validate(path string, doc *yaml.Node, value any) error {
	schema, err := compiledSchema()
	if err != nil {
		return diagnostic(path, doc, nil, "internal configuration schema error")
	}
	if err := schema.Validate(value); err != nil {
		var validation *jsonschema.ValidationError
		if errors.As(err, &validation) {
			leaf := usefulError(validation, doc)
			reason := "does not match the configuration schema"
			if _, ok := leaf.ErrorKind.(*kind.Not); ok {
				reason = "mutually exclusive fields or invalid value"
			}
			keywords := leaf.ErrorKind.KeywordPath()
			if len(keywords) > 0 {
				switch keywords[len(keywords)-1] {
				case "required":
					reason = "missing required field(s)"
					if required, ok := leaf.ErrorKind.(*kind.Required); ok {
						// These names come from our embedded schema, not YAML.
						reason += ": " + strings.Join(required.Missing, ", ")
					}
				case "additionalProperties":
					reason = "unknown or operation-inappropriate field(s)"
				case "type":
					reason = "incorrect value type"
				case "const":
					reason = "unsupported version or task type"
				case "minLength":
					reason = "must not be empty"
				case "minItems":
					reason = "must contain at least one item"
				case "minimum":
					reason = "must be nonnegative"
				case "oneOf":
					if strings.Contains(leaf.SchemaURL, "/backupTask") {
						reason = "provide exactly one archive_prefix or archive_name"
					} else {
						reason = "invalid task type or assertion fields"
					}
				case "anyOf":
					reason = "missing required alternative fields"
					if strings.Contains(leaf.SchemaURL, "/pruneTask") {
						reason = "prune requires at least one effective retention rule"
					}
				case "not":
					reason = "mutually exclusive fields or invalid value"
				case "pattern":
					reason = "invalid value format"
				}
			}
			return diagnostic(path, at(doc, leaf.InstanceLocation), safeFields(leaf.InstanceLocation), reason)
		}
		return diagnostic(path, doc, nil, "invalid configuration")
	}
	return integerRanges(path, doc, value, nil)
}

// Select a leaf in the matching task branch, not a complaint about another
// variant's discriminator. Never format a validator error or its rejected value.
func usefulError(err *jsonschema.ValidationError, doc *yaml.Node) *jsonschema.ValidationError {
	switch err.ErrorKind.(type) {
	case *kind.AnyOf:
		if strings.Contains(err.SchemaURL, "/pruneTask") {
			return err
		}
	case *kind.OneOf:
		if strings.Contains(err.SchemaURL, "/backupTask") {
			return err
		}
	}
	for _, cause := range err.Causes {
		skip := false
		location := cause.SchemaURL
		if reference, ok := cause.ErrorKind.(*kind.Reference); ok {
			location = reference.URL
		}
		for _, entry := range []struct{ schema, task string }{{"backup", "backup"}, {"prune", "prune"}, {"compact", "compact"}, {"check", "check"}, {"recoveryCheck", "recovery_check"}, {"command", "command"}} {
			if strings.Contains(location, "/"+entry.schema+"Task") {
				fields := cause.InstanceLocation
				if len(fields) >= 6 && fields[0] == "workflows" && fields[2] == "stages" && fields[4] == "tasks" {
					typeNode := at(doc, append(append([]string{}, fields[:6]...), "type"))
					skip = typeNode.Value != entry.task
				}
			}
		}
		if !skip {
			return usefulError(cause, doc)
		}
	}
	return err
}

func integerRanges(path string, doc *yaml.Node, value any, fields []string) error {
	switch value := value.(type) {
	case json.Number:
		if _, err := strconv.ParseInt(string(value), 10, strconv.IntSize); err != nil {
			return diagnostic(path, at(doc, fields), safeFields(fields), "numeric value outside supported integer range")
		}
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := integerRanges(path, doc, value[key], append(append([]string{}, fields...), key)); err != nil {
				return err
			}
		}
	case []any:
		for i, entry := range value {
			if err := integerRanges(path, doc, entry, append(append([]string{}, fields...), strconv.Itoa(i))); err != nil {
				return err
			}
		}
	}
	return nil
}
