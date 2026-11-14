package configschema

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

const schemaURL = "https://raw.githubusercontent.com/Besi97/cairn-backup/main/cli/schema/config-v1.schema.json"

// These helpers validate the published artifacts, not runtime configuration.
// Loading, discovery, secret handling, and semantic validation belong to #53.
func loadSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile("../../schema/config-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func yamlValue(t *testing.T, text string) any {
	t.Helper()
	var value any
	if err := yaml.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	// Validate YAML's JSON-compatible representation with exact JSON numbers.
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	value, err = jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func taskDocument(t *testing.T, task string) map[string]any {
	t.Helper()
	return map[string]any{
		"version":      1,
		"repositories": map[string]any{"local": map[string]any{"location": "./repo"}},
		"workflows": map[string]any{
			"manual": map[string]any{
				"stages": []any{
					map[string]any{"tasks": []any{yamlValue(t, task)}},
				},
			},
		},
	}
}

func baseDocument(t *testing.T) map[string]any {
	t.Helper()
	return taskDocument(t, "{type: compact, repository: local}")
}

func repository(doc map[string]any) map[string]any {
	return doc["repositories"].(map[string]any)["local"].(map[string]any)
}

func workflow(doc map[string]any) map[string]any {
	return doc["workflows"].(map[string]any)["manual"].(map[string]any)
}

func stage(doc map[string]any) map[string]any {
	return workflow(doc)["stages"].([]any)[0].(map[string]any)
}
