package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func minimalDocument(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/valid/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func validateDocument(t *testing.T, document string) error {
	t.Helper()
	c, err := decode("config.yaml", []byte(document))
	if err != nil {
		return err
	}
	return c.Validate()
}

func TestValidFixtures(t *testing.T) {
	paths, err := filepath.Glob("testdata/valid/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("fixtures: %v", err)
	}
	paths = append(paths, "../../../docs/config.example.yaml")
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			if _, err := LoadValidated(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvalidSemantics(t *testing.T) {
	base := minimalDocument(t)
	for _, test := range []struct{ name, old, replacement, want string }{
		{"missing version", "version: 1", "", "version"},
		{"unsupported version", "version: 1", "version: 2", "version"},
		{"empty repositories", "repositories:\n  local:\n    location: ./repository", "repositories: {}", "at least one repository"},
		{"empty jobs", base[strings.Index(base, "jobs:"):], "jobs: {}", "at least one job"},
		{"invalid repository name", "  local:", "  BAD:", "invalid name"},
		{"invalid job name", "  service:", "  BAD:", "invalid name"},
		{"missing location", "location: ./repository", "lock_wait: 60s", "location"},
		{"local identity", "location: ./repository", "location: ./repository\n    identity_file: ./key", "identity_file"},
		{"unknown repository", "repository: local", "repository: absent", "configured repository"},
		{"missing repository", "repository: local", "", "repository"},
		{"missing source root", "source_root: ./data", "", "source_root"},
		{"empty source root", "source_root: ./data", "source_root: ''", "source_root"},
		{"missing sources", "sources: [service]", "", "sources"},
		{"empty sources", "sources: [service]", "sources: []", "sources"},
		{"empty source", "sources: [service]", "sources: ['']", "sources[0]"},
		{"absolute source", "sources: [service]", "sources: [/data]", "sources[0]"},
		{"parent source", "sources: [service]", "sources: [data/../other]", "sources[0]"},
		{"trailing parent source", "sources: [service]", "sources: [data/..]", "sources[0]"},
		{"duplicate source", "sources: [service]", "sources: [service, service]", "duplicate source"},
		{"missing prefix", "archive_prefix: service", "", "archive_prefix"},
		{"invalid prefix", "archive_prefix: service", "archive_prefix: INVALID", "archive_prefix"},
		{"empty maintenance", "archive_prefix: service", "archive_prefix: service\n    maintenance: {}", "maintenance"},
		{"compact false", "archive_prefix: service", "archive_prefix: service\n    maintenance:\n      compact: false", "maintenance"},
		{"empty retention", "archive_prefix: service", "archive_prefix: service\n    maintenance:\n      retention: {}", "retention"},
		{"empty warning policy", "archive_prefix: service", "archive_prefix: service\n    maintenance:\n      compact: true\n      after_backup_warning: ''", "after_backup_warning"},
		{"invalid warning policy", "archive_prefix: service", "archive_prefix: service\n    maintenance:\n      compact: true\n      after_backup_warning: other", "after_backup_warning"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateDocument(t, strings.Replace(base, test.old, test.replacement, 1))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
		})
	}
}

func TestLockWaitBounds(t *testing.T) {
	base := minimalDocument(t)
	for _, value := range []string{"1s", "60s", "99s", "100s", "599s", "600s", "1m", "10m", "", "0s", "601s", "11m", "01s", "1.5s", "1m30s", "1h", "-1s"} {
		t.Run(value, func(t *testing.T) {
			document := strings.Replace(base, "location: ./repository", "location: ./repository\n    lock_wait: '"+value+"'", 1)
			err := validateDocument(t, document)
			valid := value == "1s" || value == "60s" || value == "99s" || value == "100s" || value == "599s" || value == "600s" || value == "1m" || value == "10m"
			if (err == nil) != valid {
				t.Fatalf("error = %v; valid = %v", err, valid)
			}
		})
	}
}

func TestSchedules(t *testing.T) {
	base := minimalDocument(t)
	for _, test := range []struct {
		cron, zone string
		valid      bool
	}{
		{"0 0 4 * * *", "", true},
		{"*/10 0-30 4 * JAN MON-FRI", "Etc/UTC", true},
		{"0 0 4 * * *", "America/New_York", true},
		{"0 0 4 * * *", "UTC", true},
		{"", "", false}, {"0 4 * * *", "", false}, {"0 0 4 * * * *", "", false},
		{"60 0 4 * * *", "", false}, {"0 60 4 * * *", "", false}, {"0 0 24 * * *", "", false},
		{"0 0 4 32 * *", "", false}, {"0 0 4 * 13 *", "", false}, {"0 0 4 * * 7", "", false},
		{"@daily", "", false}, {"TZ=UTC 0 4 * * *", "", false}, {"CRON_TZ=UTC 0 4 * * *", "", false},
		{"0 0 4 * * *", "Local", false}, {"0 0 4 * * *", "Not/AZone", false},
	} {
		t.Run(test.cron+test.zone, func(t *testing.T) {
			document := base + "    schedule:\n      cron: '" + test.cron + "'\n"
			if test.zone != "" {
				document += "      timezone: '" + test.zone + "'\n"
			}
			err := validateDocument(t, document)
			if (err == nil) != test.valid {
				t.Fatalf("error = %v, valid = %v", err, test.valid)
			}
		})
	}
	if err := validateDocument(t, base+"    schedule:\n      cron: '0 0 4 * * *'\n      timezone: ''\n"); err == nil {
		t.Fatal("accepted empty explicit timezone")
	}
}

func TestRetention(t *testing.T) {
	base := minimalDocument(t) + "    maintenance:\n      retention:\n"
	for _, field := range []string{"keep_hourly", "keep_daily", "keep_weekly", "keep_monthly", "keep_yearly"} {
		for _, count := range []string{"-1", "0", "1", "100"} {
			err := validateDocument(t, base+"        "+field+": "+count+"\n")
			if (err == nil) != (count == "1" || count == "100") {
				t.Errorf("%s %s: %v", field, count, err)
			}
		}
	}
	for _, value := range []string{"1H", "7d", "4w", "12m", "3y", "", "0d", "01d", "1h", "1s", "1.5d", "-1d"} {
		err := validateDocument(t, base+"        keep_within: '"+value+"'\n")
		valid := value == "1H" || value == "7d" || value == "4w" || value == "12m" || value == "3y"
		if (err == nil) != valid {
			t.Errorf("keep_within %q: %v", value, err)
		}
	}
}

func TestArchivePrefixUniqueness(t *testing.T) {
	base := minimalDocument(t)
	second := "  second:\n    repository: local\n    source_root: ./data\n    sources: [other]\n    archive_prefix: service\n"
	if err := validateDocument(t, base+second); err == nil {
		t.Fatal("accepted shared prefix in same repository")
	}
	base = strings.Replace(base, "jobs:", "  offsite:\n    location: host.example:repo\njobs:", 1)
	if err := validateDocument(t, base+strings.Replace(second, "repository: local", "repository: offsite", 1)); err != nil {
		t.Fatal(err)
	}
}

func TestValidationIsAggregatedStableAndSecretSafe(t *testing.T) {
	document := strings.ReplaceAll(minimalDocument(t), "local", "LEAK-secret")
	document = strings.Replace(document, "archive_prefix: service", "archive_prefix: LEAK-secret\n    schedule:\n      cron: LEAK-secret\n      timezone: LEAK-secret\n    maintenance:\n      retention:\n        keep_within: LEAK-secret\n      after_backup_warning: LEAK-secret", 1)
	var first string
	for i := 0; i < 10; i++ {
		err := validateDocument(t, document)
		if err == nil {
			t.Fatal("accepted invalid configuration")
		}
		message := err.Error()
		if strings.Contains(message, "LEAK") {
			t.Fatalf("leaked value: %s", message)
		}
		if strings.Count(message, "\n") < 5 {
			t.Fatalf("errors not aggregated: %s", message)
		}
		if i == 0 {
			first = message
		} else if message != first {
			t.Fatal("unstable diagnostic ordering")
		}
	}
}

func TestValidateBeforeResolutionAndNoReferencedFileAccess(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "config.yaml")
	document := strings.Replace(minimalDocument(t), "source_root: ./data", "source_root: $UNSET_FOR_VALIDATION", 1)
	document = strings.Replace(document, "archive_prefix: service", "archive_prefix: BAD", 1)
	writeFile(t, file, document)
	env := testEnvironment(root, nil, "")
	_, err := env.LoadValidated(file)
	if err == nil || !strings.Contains(err.Error(), "archive_prefix") {
		t.Fatalf("expected validation before expansion: %v", err)
	}
	document = minimalDocument(t)
	document = strings.Replace(document, "location: ./repository", "location: ./missing-repository\n    passphrase_file: ./secret", 1)
	writeFile(t, file, document)
	// A directory would fail ReadSecret. Neither it nor the missing repository
	// or sources should be accessed during static validation.
	if err := os.Mkdir(filepath.Join(root, "secret"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := env.LoadValidated(file); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNilAndEmptyDefinitions(t *testing.T) {
	if err := (*Config)(nil).Validate(); err == nil {
		t.Fatal("accepted nil configuration")
	}
	c := &Config{Version: 1, Repositories: map[string]*Repository{"local": nil}, Jobs: map[string]*Job{"service": nil}}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted nil definitions")
	}
}

func TestExpansionDiagnosticsDoNotEchoValues(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "config.yaml")
	env := testEnvironment(root, nil, "")
	for _, value := range []string{"LEAK/$UNSET", "${LEAK", "${LEAK-invalid}", "$(LEAK)"} {
		document := strings.Replace(minimalDocument(t), "source_root: ./data", "source_root: '"+value+"'", 1)
		writeFile(t, file, document)
		_, err := env.LoadValidated(file)
		if err == nil || !strings.Contains(err.Error(), `job "service" source_root`) {
			t.Fatalf("error lacks context: %v", err)
		}
		if strings.Contains(err.Error(), "LEAK") {
			t.Fatalf("echoed path value: %v", err)
		}
	}
}

func TestDefaultsAndOptionalCombinations(t *testing.T) {
	for _, maintenance := range []string{
		"      compact: true\n",
		"      retention:\n        keep_daily: 1\n      compact: false\n",
		"      compact: true\n      after_backup_warning: skip\n",
	} {
		document := minimalDocument(t) + "    maintenance:\n" + maintenance
		c, err := decode("config.yaml", []byte(document))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Repositories["local"].LockWait != "60s" || c.Jobs["service"].Maintenance.AfterBackupWarning != "skip" {
			t.Fatal("omitted defaults not applied")
		}
	}
}

func TestIdentifierBoundaries(t *testing.T) {
	for _, value := range []string{"a", "a0-b", "a" + strings.Repeat("b", 62), "a" + strings.Repeat("b", 63), "0abc", "Abc", "a_b", "a.b", ""} {
		valid := value == "a" || value == "a0-b" || value == "a"+strings.Repeat("b", 62)
		document := strings.Replace(minimalDocument(t), "archive_prefix: service", "archive_prefix: '"+value+"'", 1)
		if err := validateDocument(t, document); (err == nil) != valid {
			t.Errorf("prefix length %d: %v", len(value), err)
		}
	}
}
