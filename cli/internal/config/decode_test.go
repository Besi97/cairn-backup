package config

import (
	"strings"
	"testing"
)

func TestStructureRejectsUnknownFieldsAtEveryLevel(t *testing.T) {
	for _, document := range []string{
		"typo: true",
		"repositories:\n  local:\n    passphase_file: ./secret",
		"jobs:\n  service:\n    typo: true",
		"jobs:\n  service:\n    schedule:\n      typo: true",
		"jobs:\n  service:\n    maintenance:\n      typo: true",
		"jobs:\n  service:\n    maintenance:\n      retention:\n        typo: true",
	} {
		t.Run(document, func(t *testing.T) {
			_, err := decode("config.yaml", []byte(document))
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("decode error = %v, want unknown field", err)
			}
		})
	}
}

func TestStructureRejectsNullThroughoutDocument(t *testing.T) {
	for _, document := range []string{
		"null", "version: null", "repositories: null", "jobs: null",
		"repositories:\n  local: null",
		"repositories:\n  local:\n    location: null",
		"jobs:\n  service: null",
		"jobs:\n  service:\n    repository: null",
		"jobs:\n  service:\n    source_root: null",
		"jobs:\n  service:\n    archive_prefix: null",
		"jobs:\n  service:\n    sources: null",
		"jobs:\n  service:\n    sources: [null]",
		"jobs:\n  service:\n    schedule: null",
		"jobs:\n  service:\n    schedule:\n      cron: null",
		"jobs:\n  service:\n    schedule:\n      timezone: null",
		"jobs:\n  service:\n    maintenance: null",
		"jobs:\n  service:\n    maintenance:\n      compact: null",
		"jobs:\n  service:\n    maintenance:\n      after_backup_warning: null",
		"jobs:\n  service:\n    maintenance:\n      retention: null",
		"jobs:\n  service:\n    maintenance:\n      retention:\n        keep_daily: null",
	} {
		t.Run(document, func(t *testing.T) {
			_, err := decode("config.yaml", []byte(document))
			if err == nil || !strings.Contains(err.Error(), "must not be null") {
				t.Fatalf("decode error = %v, want null rejection", err)
			}
		})
	}
}

func TestStructureRejectsWrongTypesAndDuplicateKeys(t *testing.T) {
	for _, document := range []string{
		"version: '1'",
		"repositories:\n  local:\n    location: 123",
		"repositories:\n  local:\n    lock_wait: true",
		"jobs:\n  service:\n    sources: [123]",
		"jobs:\n  service:\n    maintenance:\n      compact: 'false'",
		"jobs:\n  service:\n    maintenance:\n      retention:\n        keep_daily: 1.5",
		"repositories:\n  local: {}\n  local: {}",
		"repositories:\n  local:\n    location: a\n    location: b",
		"jobs:\n  service:\n    schedule:\n      cron: a\n      cron: b",
	} {
		t.Run(document, func(t *testing.T) {
			if _, err := decode("config.yaml", []byte(document)); err == nil {
				t.Fatal("decode accepted wrong types or duplicate keys")
			}
		})
	}
}

func TestDecodeDiagnosticsDoNotEchoInput(t *testing.T) {
	// A short marker catches both complete and truncated scalar leakage. The
	// embedded backtick would defeat the former regexp-based sanitizer.
	const marker = "LEAK"
	for _, document := range []string{
		"version: LEAK",
		"version: 'x`LEAK'",
		"version: LEAK-long-value-that-the-decoder-truncates",
		"LEAK: true",
		"LEAK: true\nLEAK: false",
		"repositories:\n  LEAK:\n    location: 123",
		"version: *LEAK",
		"version: !LEAK [",
		"version: 1\n---\nversion: *LEAK",
	} {
		t.Run(document, func(t *testing.T) {
			_, err := decode("config.yaml", []byte(document))
			if err == nil {
				t.Fatal("decode succeeded, want error")
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("diagnostic echoed configuration input: %v", err)
			}
		})
	}
}

func TestStructureDiagnosticsHaveLineAndFieldContext(t *testing.T) {
	_, err := decode("config.yaml", []byte("jobs:\n  service:\n    maintenance:\n      compact: 'false'"))
	if err == nil || !strings.Contains(err.Error(), "line 4") || !strings.Contains(err.Error(), "jobs[entry].maintenance.compact") {
		t.Fatalf("decode error lacks line/field context: %v", err)
	}
}

func TestDecodeAliasesRemainStrict(t *testing.T) {
	config, err := decode("config.yaml", []byte(`repositories:
  local: &repository
    location: ./local
  second: *repository
`))
	if err != nil {
		t.Fatalf("decode valid alias: %v", err)
	}
	for _, name := range []string{"local", "second"} {
		if config.Repositories[name].LockWait != "60s" {
			t.Errorf("repository %s: default not applied", name)
		}
	}
	for _, document := range []string{
		"repositories:\n  local: &repository\n    typo: true\n  second: *repository",
		"repositories:\n  local: &repository\n    location: null\n  second: *repository",
		"jobs:\n  service: &job\n    schedule: *job",
	} {
		if _, err := decode("config.yaml", []byte(document)); err == nil {
			t.Fatal("decode accepted an invalid alias")
		}
	}
}
