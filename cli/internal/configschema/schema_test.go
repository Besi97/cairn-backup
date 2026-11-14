// Package configschema tests the published configuration contract independently
// of the future runtime loader and without Borg or repository access.
package configschema

import (
	"fmt"
	"os"
	"testing"
)

func TestExample(t *testing.T) {
	schema := loadSchema(t)
	data, err := os.ReadFile("../../examples/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(yamlValue(t, string(data))); err != nil {
		t.Fatal(err)
	}
}

func TestValidTasks(t *testing.T) {
	schema := loadSchema(t)
	for _, task := range []string{
		`{type: backup, repository: local, archive_prefix: documents, sources: [.]}`,
		`{type: backup, repository: local, archive_name: "{hostname}-before-upgrade", sources: [., /srv, ../other, "-source"]}`,
		`{type: backup, repository: local, archive_prefix: documents, source_root: "~/Documents", sources: [.], exclude: ["*.tmp", "re:cache", ""], lock_wait: 0}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [.], exclude: []}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: 7}`,
		`{type: prune, repository: local, archive_prefix: "{hostname}-*", keep_within: 2d, keep_daily: 0, keep_weekly: 4, lock_wait: 60}`,
		`{type: compact, repository: local}`,
		`{type: compact, repository: local, lock_wait: 0}`,
	} {
		t.Run(task, func(t *testing.T) {
			if err := schema.Validate(taskDocument(t, task)); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, rule := range []string{"secondly", "minutely", "hourly", "daily", "weekly", "monthly", "yearly"} {
		for _, count := range []int{-1, 1, 7} {
			task := fmt.Sprintf("{type: prune, repository: local, archive_prefix: documents, keep_%s: %d}", rule, count)
			t.Run(task, func(t *testing.T) {
				if err := schema.Validate(taskDocument(t, task)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	for _, interval := range []string{"1H", "7d", "4w", "12m", "2y", "+01d", "0002d", "999999999999999999999999999y"} {
		task := fmt.Sprintf("{type: prune, repository: local, archive_prefix: documents, keep_within: %q}", interval)
		t.Run(task, func(t *testing.T) {
			if err := schema.Validate(taskDocument(t, task)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvalidTasks(t *testing.T) {
	schema := loadSchema(t)
	for _, task := range []string{
		`null`,
		`[]`,
		`{}`,
		`{repository: local}`,
		`{type: unknown, repository: local}`,
		`{type: compact}`,
		`{type: compact, repository: ""}`,
		`{type: compact, repository: null}`,
		`{type: compact, repository: 1}`,
		`{type: compact, repository: local, name: maintenance}`,
		`{type: compact, repository: local, archive_prefix: documents}`,
		`{type: compact, repository: local, keep_daily: 1}`,
		`{type: compact, repository: local, sources: [.]}`,
		`{type: compact, repository: local, threshold: 0}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [.], keep_daily: 1}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [.], backup: {}}`,
		`{type: backup, repository: local, sources: [.]}`,
		`{type: backup, repository: local, archive_prefix: a, archive_name: b, sources: [.]}`,
		`{type: backup, repository: local, archive_prefix: "", sources: [.]}`,
		`{type: backup, repository: local, archive_name: "", sources: [.]}`,
		`{type: backup, repository: local, archive_prefix: documents}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: []}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [""]}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [null]}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: "."}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [.], source_root: ""}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [.], source_root: ~}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [.], exclude: "*.tmp"}`,
		`{type: backup, repository: local, archive_prefix: documents, sources: [.], exclude: [null]}`,
		`{type: prune, repository: local, keep_daily: 7}`,
		`{type: prune, repository: local, archive_prefix: "", keep_daily: 7}`,
		`{type: prune, repository: local, archive_prefix: documents}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: 0, keep_weekly: 0}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: "7"}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: 1.5}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: null}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: true}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: 7, sources: [.]}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: 7, archive_name: exact}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: 7, retention: {}}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_quarterly: 1}`,
	} {
		t.Run(task, func(t *testing.T) {
			if err := schema.Validate(taskDocument(t, task)); err == nil {
				t.Fatal("schema accepted invalid task")
			}
		})
	}
	for _, rule := range []string{"secondly", "minutely", "hourly", "daily", "weekly", "monthly", "yearly"} {
		task := fmt.Sprintf("{type: prune, repository: local, archive_prefix: documents, keep_%s: 0}", rule)
		t.Run(task, func(t *testing.T) {
			if err := schema.Validate(taskDocument(t, task)); err == nil {
				t.Fatal("schema accepted zero-only policy")
			}
		})
	}
	for _, interval := range []string{"", "0d", "+000H", "-1d", "1h", "1.5d", "1", "2D", " 1d", "1d\n", "1d\r\n"} {
		task := fmt.Sprintf("{type: prune, repository: local, archive_prefix: documents, keep_within: %q}", interval)
		t.Run(task, func(t *testing.T) {
			if err := schema.Validate(taskDocument(t, task)); err == nil {
				t.Fatal("schema accepted invalid interval")
			}
		})
	}
}

func TestTaskCommonFields(t *testing.T) {
	schema := loadSchema(t)
	for _, task := range []string{
		`{type: backup, repository: local, archive_prefix: documents, sources: [.]}`,
		`{type: prune, repository: local, archive_prefix: documents, keep_daily: 7}`,
		`{type: compact, repository: local}`,
	} {
		for _, extra := range []string{
			`lock_wait: -1`, `lock_wait: 1.5`, `lock_wait: "60"`, `lock_wait: null`,
			`dry_run: true`, `dry_run: false`, `unknown: true`,
			`passphrase: secret`, `passphrase_file: ./secret`,
			`identity_file: ./identity`, `location: ./repo`,
		} {
			t.Run(task+" "+extra, func(t *testing.T) {
				value := yamlValue(t, task).(map[string]any)
				for key, entry := range yamlValue(t, "{"+extra+"}").(map[string]any) {
					value[key] = entry
				}
				doc := baseDocument(t)
				stage(doc)["tasks"] = []any{value}
				if err := schema.Validate(doc); err == nil {
					t.Fatal("schema accepted invalid common field")
				}
			})
		}
	}
}

func TestDocumentStructure(t *testing.T) {
	schema := loadSchema(t)
	for _, tt := range []struct {
		name   string
		valid  bool
		change func(map[string]any)
	}{
		{"empty maps", true, func(d map[string]any) {
			d["repositories"], d["workflows"] = map[string]any{}, map[string]any{}
		}},
		{"scheduled workflow", true, func(d map[string]any) { workflow(d)["cron"] = "0 2 * * *" }},
		{"inline secret", true, func(d map[string]any) { repository(d)["passphrase"] = "${LITERAL}~" }},
		{"empty explicit secret", true, func(d map[string]any) { repository(d)["passphrase"] = "" }},
		{"file secret", true, func(d map[string]any) { repository(d)["passphrase_file"] = "./secret" }},
		{"SSH identity", true, func(d map[string]any) {
			repository(d)["location"] = "backup@host:repo"
			repository(d)["identity_file"] = "~/.ssh/cairn"
		}},
		{"parallel tasks", true, func(d map[string]any) {
			stage(d)["tasks"] = []any{
				yamlValue(t, "{type: compact, repository: local}"),
				yamlValue(t, "{type: compact, repository: local}"),
			}
		}},
		{"ordered stages", true, func(d map[string]any) {
			workflow(d)["stages"] = []any{stage(d), stage(d)}
		}},
		{"missing version", false, func(d map[string]any) { delete(d, "version") }},
		{"unsupported version", false, func(d map[string]any) { d["version"] = 2 }},
		{"string version", false, func(d map[string]any) { d["version"] = "1" }},
		{"null version", false, func(d map[string]any) { d["version"] = nil }},
		{"missing repositories", false, func(d map[string]any) { delete(d, "repositories") }},
		{"missing workflows", false, func(d map[string]any) { delete(d, "workflows") }},
		{"old jobs field", false, func(d map[string]any) { d["jobs"] = map[string]any{} }},
		{"unknown top level", false, func(d map[string]any) { d["extra"] = true }},
		{"repository list", false, func(d map[string]any) { d["repositories"] = []any{} }},
		{"null repositories", false, func(d map[string]any) { d["repositories"] = nil }},
		{"workflow list", false, func(d map[string]any) { d["workflows"] = []any{} }},
		{"null workflows", false, func(d map[string]any) { d["workflows"] = nil }},
		{"empty repository name", false, func(d map[string]any) {
			d["repositories"].(map[string]any)[""] = repository(d)
		}},
		{"empty workflow name", false, func(d map[string]any) {
			d["workflows"].(map[string]any)[""] = workflow(d)
		}},
		{"missing location", false, func(d map[string]any) { delete(repository(d), "location") }},
		{"empty location", false, func(d map[string]any) { repository(d)["location"] = "" }},
		{"null location", false, func(d map[string]any) { repository(d)["location"] = nil }},
		{"unknown repository field", false, func(d map[string]any) { repository(d)["extra"] = true }},
		{"both credential forms", false, func(d map[string]any) {
			repository(d)["passphrase"], repository(d)["passphrase_file"] = "", "./secret"
		}},
		{"null secret", false, func(d map[string]any) { repository(d)["passphrase"] = nil }},
		{"numeric secret", false, func(d map[string]any) { repository(d)["passphrase"] = 123 }},
		{"empty secret file", false, func(d map[string]any) { repository(d)["passphrase_file"] = "" }},
		{"null secret file", false, func(d map[string]any) { repository(d)["passphrase_file"] = nil }},
		{"empty identity file", false, func(d map[string]any) { repository(d)["identity_file"] = "" }},
		{"null identity file", false, func(d map[string]any) { repository(d)["identity_file"] = nil }},
		{"unknown workflow field", false, func(d map[string]any) { workflow(d)["extra"] = true }},
		{"empty cron", false, func(d map[string]any) { workflow(d)["cron"] = "" }},
		{"numeric cron", false, func(d map[string]any) { workflow(d)["cron"] = 123 }},
		{"null cron", false, func(d map[string]any) { workflow(d)["cron"] = nil }},
		{"missing stages", false, func(d map[string]any) { delete(workflow(d), "stages") }},
		{"empty stages", false, func(d map[string]any) { workflow(d)["stages"] = []any{} }},
		{"null stages", false, func(d map[string]any) { workflow(d)["stages"] = nil }},
		{"named stages", false, func(d map[string]any) { workflow(d)["stages"] = map[string]any{} }},
		{"named stage", false, func(d map[string]any) { stage(d)["name"] = "maintenance" }},
		{"unknown stage field", false, func(d map[string]any) { stage(d)["extra"] = true }},
		{"missing tasks", false, func(d map[string]any) { delete(stage(d), "tasks") }},
		{"empty tasks", false, func(d map[string]any) { stage(d)["tasks"] = []any{} }},
		{"null tasks", false, func(d map[string]any) { stage(d)["tasks"] = nil }},
		{"named tasks", false, func(d map[string]any) { stage(d)["tasks"] = map[string]any{} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := baseDocument(t)
			tt.change(doc)
			err := schema.Validate(doc)
			if (err == nil) != tt.valid {
				t.Fatalf("validation error = %v, want valid %v", err, tt.valid)
			}
		})
	}
	for _, document := range []string{"null", "[]", "{}", "1", "text"} {
		t.Run("root "+document, func(t *testing.T) {
			if err := schema.Validate(yamlValue(t, document)); err == nil {
				t.Fatal("schema accepted invalid document")
			}
		})
	}
}

func TestRuntimeValidationBoundary(t *testing.T) {
	// These are intentionally accepted structurally. #53/#55 must validate
	// references and operation/schedule semantics; schema success is not enough.
	schema := loadSchema(t)
	doc := taskDocument(t, "{type: compact, repository: not-defined}")
	workflow(doc)["cron"] = "not-a-cron-expression"
	repository(doc)["identity_file"] = "./identity-for-local-repo"
	if err := schema.Validate(doc); err != nil {
		t.Fatal(err)
	}
}
