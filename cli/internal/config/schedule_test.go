package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestScheduleValidation(t *testing.T) {
	for _, tt := range []struct {
		name, fields, want string
	}{
		{"five fields", "cron: '0 2 * * *'", ""},
		{"alias and timezone", "cron: '@daily'\n    timezone: Europe/Berlin", ""},
		{"timezone without cron", "timezone: UTC", ""},
		{"empty cron", "cron: ''", "/cron"},
		{"whitespace cron", "cron: '   '", "/cron"},
		{"range", "cron: '60 2 * * *'", "/cron"},
		{"seconds", "cron: '0 0 2 * * *'", "/cron"},
		{"interval", "cron: '@every 1h'", "/cron"},
		{"inline timezone", "cron: 'CRON_TZ=UTC 0 2 * * *'", "/cron"},
		{"empty timezone", "timezone: ''", "/timezone"},
		{"null timezone", "timezone: null", "/timezone"},
		{"numeric timezone", "timezone: 123", "/timezone"},
		{"unknown timezone", "timezone: Europe/Nowhere", "/timezone"},
		{"local keyword", "timezone: Local", "/timezone"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text := strings.Replace(withTask("{type: compact, repository: local}"), "    stages:",
				"    "+tt.fields+"\n    stages:", 1)
			config, err := loadText(t, text)
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v; want %s", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.name == "alias and timezone" {
				w := config.Workflows["manual"]
				if w.Cron != "@daily" || w.Timezone != "Europe/Berlin" {
					t.Fatalf("schedule fields lost: %+v", w)
				}
			}
		})
	}
}

func TestScheduleDiagnostics(t *testing.T) {
	const secret = "never-print-this-secret"
	for _, field := range []string{"cron", "timezone"} {
		text := strings.Replace(withTask("{type: compact, repository: local}"),
			"    stages:", fmt.Sprintf("    %s: %q\n    stages:", field, secret), 1)
		_, err := loadText(t, text)
		if err == nil || strings.Contains(err.Error(), secret) ||
			!strings.Contains(err.Error(), "config.yaml:6:") ||
			!strings.Contains(err.Error(), "/workflows/manual/"+field+":") {
			t.Fatalf("%s diagnostic = %v", field, err)
		}
	}
}
