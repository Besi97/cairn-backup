package config

import (
	"strings"
	"testing"
)

func TestRecoveryValidationAndRedaction(t *testing.T) {
	valid := `{type: recovery_check, repository: local, archive_prefix: "literal-*", paths: [docs], checks: [{kind: file_bytes, path: docs/a, bytes: "secret\n"}], sample: {live_root: ./live, limit: 1}}`
	cfg, err := loadText(t, withTask(valid))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg.SanitizedYAML(), "secret") || !strings.Contains(cfg.SanitizedYAML(), "[redacted]") {
		t.Fatal("assertion bytes exposed")
	}
	for _, task := range []string{
		`{type: recovery_check, repository: local, archive_name: a, archive_prefix: b, whole_archive: true, sample: {live_root: ., limit: 1}}`,
		`{type: recovery_check, repository: local, archive_name: a, paths: [x], whole_archive: true, sample: {live_root: ., limit: 1}}`,
		`{type: recovery_check, repository: local, archive_name: a, paths: [x]}`,
		`{type: recovery_check, repository: local, archive_name: a, paths: [../x], sample: {live_root: ., limit: 1}}`,
		`{type: recovery_check, repository: local, archive_name: "a/b", whole_archive: true, sample: {live_root: ., limit: 1}}`,
		`{type: recovery_check, repository: local, archive_name: a, whole_archive: true, checks: [{kind: file_size, path: ../x, size: 0}]}`,
		`{type: recovery_check, repository: local, archive_name: a, whole_archive: true, checks: [{kind: file_size, path: x, size: -1}]}`,
		`{type: recovery_check, repository: local, archive_name: a, whole_archive: true, checks: [{kind: file_bytes, path: x, bytes: secret, size: 1}]}`,
		`{type: recovery_check, repository: local, archive_name: a, whole_archive: true, sample: {live_root: ., limit: 10001}}`,
		`{type: recovery_check, repository: local, archive_name: a, whole_archive: true, sample: {live_root: ., limit: 1}, command: secret}`,
	} {
		t.Run(task, func(t *testing.T) {
			_, err := loadText(t, withTask(task))
			if err == nil {
				t.Fatal("accepted invalid task")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error leaked configured value")
			}
		})
	}
}
