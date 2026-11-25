package config

import (
	"os"
	"strings"
	"testing"
)

func TestInvalidConfigurations(t *testing.T) {
	for _, tt := range []struct{ name, text, want string }{
		{"empty", "", "invalid YAML"},
		{"null", "null", "incorrect value type"},
		{"missing version", "repositories: {}\nworkflows: {}", "missing required"},
		{"unsupported version", "version: 2\nrepositories: {}\nworkflows: {}", "/version"},
		{"string version", "version: '1'\nrepositories: {}\nworkflows: {}", "/version"},
		{"unknown", minimal + "extra: true\n", "unknown"},
		{"duplicate", minimal + "version: 1\n", "duplicate"},
		{"nested duplicate", strings.Replace(withTask("{type: compact, repository: local}"), "{location: ./repo}", "{location: ./repo, location: ./other}", 1), "duplicate"},
		{"numeric key", "version: 1\nrepositories: {123: {location: ./repo}}\nworkflows: {}", "keys must be strings"},
		{"documents", minimal + "---\n" + minimal, "multiple YAML documents"},
		{"empty second document", minimal + "---\n", "multiple YAML documents"},
		{"timestamp", strings.Replace(minimal, "repositories: {}", "repositories: {local: {location: 2026-01-01}}", 1), "non-JSON"},
		{"nan", strings.Replace(minimal, "version: 1", "version: .nan", 1), "non-JSON"},
		{"inf", strings.Replace(minimal, "version: 1", "version: .inf", 1), "non-JSON"},
		{"custom tag", strings.Replace(minimal, "version: 1", "version: !secret value", 1), "non-JSON"},
		{"merge", "version: 1\nrepositories: {local: {<<: {location: ./repo}}}\nworkflows: {}", "merge keys"},
		{"recursive alias", "version: 1\nrepositories: &loop {local: *loop}\nworkflows: {}", "limit"},
		{"missing repository", withTask("{type: compact, repository: missing}"), "/repository"},
		{"local identity", strings.Replace(withTask("{type: compact, repository: local}"), "{location: ./repo}", "{location: ./repo, identity_file: ./key}", 1), "/identity_file"},
		{"archive location", strings.Replace(withTask("{type: compact, repository: local}"), "./repo", "./repo::archive", 1), "/location"},
		{"empty file URL", strings.Replace(withTask("{type: compact, repository: local}"), "./repo", "'file://'", 1), "/location"},
		{"empty SSH URL", strings.Replace(withTask("{type: compact, repository: local}"), "./repo", "'ssh://'", 1), "/location"},
		{"SSH without path", strings.Replace(withTask("{type: compact, repository: local}"), "./repo", "'host:'", 1), "/location"},
		{"invalid SSH port", strings.Replace(withTask("{type: compact, repository: local}"), "./repo", "'ssh://host:invalid/repo'", 1), "/location"},
		{"credential conflict", strings.Replace(withTask("{type: compact, repository: local}"), "{location: ./repo}", "{location: ./repo, passphrase: '', passphrase_file: ./secret}", 1), "mutually exclusive"},
		{"bad task", withTask("{type: remove, repository: local}"), "task type"},
		{"missing source", withTask("{type: backup, repository: local, archive_prefix: docs}"), "missing required"},
		{"both names", withTask("{type: backup, repository: local, archive_prefix: docs, archive_name: exact, sources: [.]}"), "exactly one"},
		{"archive slash", withTask("{type: backup, repository: local, archive_name: a/b, sources: [.]}"), "/archive_name"},
		{"zero policy", withTask("{type: prune, repository: local, archive_prefix: docs, keep_daily: 0}"), "effective retention"},
		{"bad interval", withTask("{type: prune, repository: local, archive_prefix: docs, keep_within: 0d}"), "/keep_within"},
		{"overflow", withTask("{type: compact, repository: local, lock_wait: 9223372036854775808}"), "integer range"},
		{"negative overflow", withTask("{type: prune, repository: local, archive_prefix: docs, keep_daily: -9223372036854775809}"), "integer range"},
		{"negative wait", withTask("{type: compact, repository: local, lock_wait: -1}"), "/lock_wait"},
		{"fractional wait", withTask("{type: compact, repository: local, lock_wait: 1.00000000000000000001}"), "/lock_wait"},
		{"tiny fractional wait", withTask("{type: compact, repository: local, lock_wait: 1e-324}"), "/lock_wait"},
		{"dry run", withTask("{type: compact, repository: local, dry_run: true}"), "unknown"},
		{"check repository", withTask("{type: check}"), "missing required"},
		{"check reference", withTask("{type: check, repository: missing}"), "/repository"},
		{"check verify type", withTask("{type: check, repository: local, verify_data: 'true'}"), "/verify_data"},
		{"check repair", withTask("{type: check, repository: local, repair: true}"), "unknown"},
		{"check selector", withTask("{type: check, repository: local, archive_prefix: docs}"), "unknown"},
		{"check negative wait", withTask("{type: check, repository: local, lock_wait: -1}"), "/lock_wait"},
		{"command relative program", withTask("{type: command, program: ./cmd, args: [], timeout_seconds: 1}"), "/program"},
		{"command missing args", withTask("{type: command, program: /bin/true, timeout_seconds: 1}"), "missing required"},
		{"command zero timeout", withTask("{type: command, program: /bin/true, args: [], timeout_seconds: 0}"), "/timeout_seconds"},
		{"command long timeout", withTask("{type: command, program: /bin/true, args: [], timeout_seconds: 86401}"), "/timeout_seconds"},
		{"command repository", withTask("{type: command, program: /bin/true, args: [], timeout_seconds: 1, repository: local}"), "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadText(t, tt.text)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v; want %q", err, tt.want)
			}
		})
	}
}

func TestCommandWithoutRepository(t *testing.T) {
	cfg, err := loadText(t, "version: 1\nrepositories: {}\nworkflows:\n  test:\n    stages:\n      - tasks:\n          - {type: command, program: /bin/echo, args: ['a b', '$(literal)', ''], timeout_seconds: 10}\n")
	if err != nil {
		t.Fatal(err)
	}
	task := cfg.Workflows["test"].Stages[0].Tasks[0]
	if task.Program != "/bin/echo" || len(task.Args) != 3 || task.Args[1] != "$(literal)" || task.LockWait != nil {
		t.Fatalf("unexpected task: %#v", task)
	}
}

func TestErrorsNeverIncludeSecrets(t *testing.T) {
	const secret = "never-print-this-secret"
	for _, text := range []string{
		"version: [" + secret,
		"version: *" + secret,
		"version: !" + secret + " value",
		"version: 1\nrepositories: {local: {location: ./repo, passphrase: [" + secret + "]}}\nworkflows: {}",
		"version: 1\nrepositories: {local: {location: ./repo, passphrase: {" + secret + ": [1, 2]}}}\nworkflows: {}",
		"version: 1\nrepositories: {local: {location: ./repo, passphrase: " + secret + ", " + secret + ": true}}\nworkflows: {}",
		"version: 1\nrepositories: {local: {location: ./repo, passphrase: " + secret + ", passphrase_file: ./secret}}\nworkflows: {}",
	} {
		_, err := loadText(t, text)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe error = %v", err)
		}
	}
}

func TestValidConfigurations(t *testing.T) {
	for _, task := range []string{
		"{type: compact, repository: local}",
		"{type: backup, repository: local, archive_prefix: '{hostname}-docs', sources: [., ../other, /abs, -source], exclude: ['*.tmp', 're:cache']}",
		"{type: prune, repository: local, archive_prefix: '{hostname}-*', keep_daily: -1, keep_within: '+0002d'}",
		"{type: prune, repository: local, archive_prefix: docs, keep_within: 999999999999999999999999999y}",
		"{type: compact, repository: local, lock_wait: 9223372036854775807}",
		"{type: compact, repository: local, lock_wait: 1.0}",
		"{type: check, repository: local}",
		"{type: check, repository: local, verify_data: false}",
		"{type: check, repository: local, verify_data: true, lock_wait: 0}",
	} {
		t.Run(task, func(t *testing.T) {
			config, err := loadText(t, withTask(task))
			if err != nil {
				t.Fatal(err)
			}
			if firstTask(config).LockWait == nil {
				t.Fatal("lock wait default not applied")
			}
		})
	}
	config, err := loadText(t, strings.Replace(withTask("{type: compact, repository: local, lock_wait: 0}"), "    stages:", "    cron: '@daily'\n    stages:", 1))
	if err != nil || *firstTask(config).LockWait != 0 {
		t.Fatalf("explicit zero or valid cron: %v", err)
	}
	if _, err := loadText(t, "version: 1\nrepositories: {a: &repo {location: ./a}, b: *repo}\nworkflows: {}"); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedExampleLoads(t *testing.T) {
	example, err := os.ReadFile("../../examples/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadText(t, string(example)); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticLocation(t *testing.T) {
	_, err := loadText(t, "version: 2\nrepositories: {}\nworkflows: {}\n")
	if err == nil || !strings.Contains(err.Error(), "config.yaml:1:10: /version:") {
		t.Fatalf("error = %v", err)
	}
}

func TestTaskDiagnosticWithUnrestrictedWorkflowNames(t *testing.T) {
	for _, name := range []string{"tasks", "stages", "version", "123", "backup server"} {
		text := strings.Replace(withTask("{type: compact, repository: local, lock_wait: -1}"), "  manual:", "  "+name+":", 1)
		if name == "123" {
			text = strings.Replace(text, "  123:", "  '123':", 1)
		}
		_, err := loadText(t, text)
		if err == nil || !strings.Contains(err.Error(), "/lock_wait") {
			t.Fatalf("name=%q error=%v", name, err)
		}
	}
}
