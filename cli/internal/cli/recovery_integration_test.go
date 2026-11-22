package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/history"
)

func TestRealBorgConfiguredRecovery(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	base := t.TempDir()
	t.Chdir(base)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	repo := filepath.Join(base, "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repo)
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("payload", "old\n")
	borgOutput(t, ctx, binary, "", "create", "--timestamp", "2020-01-01T12:00:00", repo+"::docs-old", "payload")
	write("payload", "ready\n")
	borgOutput(t, ctx, binary, "", "create", "--timestamp", "2021-01-01T12:00:00", repo+"::docs-new", "payload")
	config := `version: 1
repositories:
  local: {location: ./repo}
workflows:
  drill:
    cron: "@daily"
    stages:
      - tasks:
          - type: recovery_check
            repository: local
            archive_prefix: docs-
            paths: [payload]
            checks: [{kind: file_bytes, path: payload, bytes: "ready\n"}]
            sample: {live_root: ., limit: 1}
`
	write("config.yaml", config)
	for _, tc := range []struct {
		name, config, live, archive, check, sample string
		code                                       int
	}{
		{"pass", config, "ready\n", "docs-new", "passed", "passed", 0},
		{"fixed expectation", strings.Replace(config, "archive_prefix: docs-", "archive_name: docs-old", 1), "old\n", "docs-old", "mismatch", "passed", 2},
		{"live drift", config, "changed\n", "docs-new", "passed", "different", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write("config.yaml", tc.config)
			write("payload", tc.live)
			var out, errout bytes.Buffer
			code := Run(ctx, []string{"workflow", "run", "drill", "--config", filepath.Join(base, "config.yaml")}, &out, &errout, "test")
			if code != tc.code {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &out, &errout)
			}
			if !strings.Contains(out.String()+errout.String(), "Recovery evidence v1") {
				t.Fatalf("missing report: %s %s", &out, &errout)
			}
			entries, err := os.ReadDir(temp)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("workspace left behind: %v", entries)
			}
			out.Reset()
			errout.Reset()
			if code := Run(ctx, []string{"history", "--json"}, &out, &errout, "test"); code != 0 {
				t.Fatalf("history %d: %s", code, &errout)
			}
			var doc struct {
				Runs []struct {
					Summary history.Summary `json:"summary"`
				} `json:"runs"`
			}
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Runs) == 0 || len(doc.Runs[0].Summary.Recovery) != 1 {
				t.Fatalf("history: %s", &out)
			}
			e := doc.Runs[0].Summary.Recovery[0].Evidence
			if e.Details == nil || e.Details.ArchiveName != `"`+tc.archive+`"` || len(e.Details.ArchiveID) != 64 || len(e.Details.Checks) != 1 || e.Details.Checks[0].Status != tc.check || e.Sample.Status != tc.sample || e.Sample.Compared != 1 || e.Selection.Status != "passed" || e.IdentityBefore.Status != "passed" || e.Extraction.Status != "passed" || e.IdentityAfter.Status != "passed" || e.Cleanup.Status != "passed" {
				t.Fatalf("unexpected evidence: %+v details=%+v", e, e.Details)
			}
			out.Reset()
			errout.Reset()
			if code := Run(ctx, []string{"history"}, &out, &errout, "test"); code != 0 || !strings.Contains(out.String(), `archive="`+tc.archive+`"`) {
				t.Fatalf("human history %d: %s %s", code, &out, &errout)
			}
		})
	}
}
