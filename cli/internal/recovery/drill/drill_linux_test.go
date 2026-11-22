package drill

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/assertions"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/report"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/selection"
)

func fixture(t *testing.T, behavior string) (borg.Runner, Options, string, string) {
	t.Helper()
	dir := t.TempDir()
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("DRILL_FIXTURE", dir)
	t.Setenv("BORG_PASSPHRASE", "")
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	if err := tw.WriteHeader(&tar.Header{Name: "file", Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	id, repo := strings.Repeat("a", 64), strings.Repeat("b", 64)
	manifest := fmt.Sprintf(`{"repository":{"id":"%s"},"archives":[{"barchive":"snapshot","id":"%s"}]}`, repo, id)
	write := func(name string, value []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), value, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("tar", data.Bytes(), 0600)
	write("manifest", []byte(manifest), 0600)
	write("borg", []byte(`#!/bin/sh
set -eu
if [ "$1" = "--version" ]; then echo "borg 1.4.5"; exit; fi
printf '%s\n' "$1" >> "$DRILL_FIXTURE/calls"
`+behavior+`
case "$1" in
  check) : ;;
  list)
    case " $* " in
      *" --json "*) cat "$DRILL_FIXTURE/manifest";;
      *) printf '%s' '-'; printf '\0'; printf '%s' '-rw-------'; printf '\0';;
    esac;;
  export-tar) cat "$DRILL_FIXTURE/tar";;
  extract) printf restored > file;;
  *) exit 2;;
esac
`), 0700)
	return borg.Runner{Executable: filepath.Join(dir, "borg")}, Options{
		Repository: borg.PreparedRepository{Location: filepath.Join(dir, "repo")},
		Dir:        dir, Selector: selection.Selector{LatestPrefix: "snap"}, Paths: []string{"file"},
		Checks: []assertions.Check{{Kind: assertions.FileBytes, Path: "file", Bytes: []byte("restored")}},
	}, dir, temp
}

func assertEvidence(t *testing.T, result Result) {
	t.Helper()
	if err := report.WriteJSON(&bytes.Buffer{}, result.Evidence); err != nil {
		t.Fatalf("unrenderable evidence: %v: %+v", err, result.Evidence)
	}
}

func TestRunSuccessAndSampleDifference(t *testing.T) {
	runner, opts, dir, temp := fixture(t, "")
	live := t.TempDir()
	if err := os.WriteFile(filepath.Join(live, "file"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	opts.Sample = &SampleOptions{LiveRoot: live, Limit: 1}
	wait := 1
	opts.LockWait = &wait
	result, err := Run(t.Context(), runner, opts, nil)
	if err != nil || !result.Warning || result.BorgCheck.Status != report.Passed || result.Evidence.Outcome.Status != report.Incomplete ||
		result.Evidence.Sample.Status != "different" || result.Evidence.Cleanup.Status != report.Passed ||
		result.Evidence.Checks[0].Status != "passed" || result.Evidence.IdentityAfter.Status != report.Passed {
		t.Fatalf("result %+v error %v", result, err)
	}
	assertEvidence(t, result)
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if string(calls) != "check\nlist\nlist\nlist\nexport-tar\nextract\nlist\n" {
		t.Fatalf("calls %q", calls)
	}
	entries, _ := os.ReadDir(temp)
	if len(entries) != 0 {
		t.Fatalf("workspace not cleaned: %v", entries)
	}
}

func TestRunFailures(t *testing.T) {
	for _, tc := range []struct {
		name, behavior string
		mutate         func(*Options)
		want           report.Status
		step           string
	}{
		{"borg warning", `if [ "$1" = check ]; then exit 1; fi`, nil, report.Failed, "borg"},
		{"missing identity", `if [ "$1" = list ]; then printf '{"repository":{},"archives":[]}'; exit; fi`, nil, report.Failed, "selection"},
		{"before changed", `if [ "$1" = check ]; then :; elif [ "$1" = list ] && [ -f "$DRILL_FIXTURE/seen" ]; then sed 's/aaaaaaaa/bbbbbbbb/' "$DRILL_FIXTURE/manifest"; exit; elif [ "$1" = list ]; then touch "$DRILL_FIXTURE/seen"; fi`, nil, report.Failed, "before"},
		{"after missing", `if [ "$1" = extract ]; then printf restored > file; printf '{"repository":{"id":"%064d"},"archives":[]}' 0 > "$DRILL_FIXTURE/manifest"; exit; fi`, nil, report.Failed, "after"},
		{"extraction warning", `if [ "$1" = extract ]; then exit 1; fi`, nil, report.Failed, "extraction"},
		{"assertion", "", func(o *Options) { o.Checks[0].Bytes = []byte("wrong") }, report.Failed, "checks"},
		{"sample error", "", func(o *Options) { o.Sample = &SampleOptions{LiveRoot: "/missing", Limit: 1} }, report.Failed, "sample"},
		{"sample omitted", "", func(o *Options) { o.Sample = &SampleOptions{LiveRoot: o.Dir, Limit: 1} }, report.Failed, "sample"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, opts, _, temp := fixture(t, tc.behavior)
			if tc.mutate != nil {
				tc.mutate(&opts)
			}
			result, err := Run(t.Context(), runner, opts, nil)
			if err == nil || result.Evidence.Outcome.Status != tc.want {
				t.Fatalf("result %+v error %v", result, err)
			}
			var got report.Status
			switch tc.step {
			case "borg":
				got = result.BorgCheck.Status
			case "selection":
				got = result.Evidence.Selection.Status
			case "before":
				got = result.Evidence.IdentityBefore.Status
			case "after":
				got = result.Evidence.IdentityAfter.Status
			case "extraction":
				got = result.Evidence.Extraction.Status
			case "checks":
				got = result.Evidence.ChecksState.Status
			case "sample":
				if result.Evidence.Outcome.Reason == "sample_failed" {
					got = report.Failed
				}
			}
			if got != report.Failed {
				t.Fatalf("wrong step: %+v", result)
			}
			if tc.step == "after" && result.Evidence.ChecksState.Status != report.NotRun {
				t.Fatal("checks ran before post-extraction identity check")
			}
			assertEvidence(t, result)
			entries, _ := os.ReadDir(temp)
			if len(entries) != 0 {
				t.Fatalf("workspace not cleaned: %v", entries)
			}
		})
	}
}

func TestCanceledAndInvalidEvidence(t *testing.T) {
	runner, opts, _, _ := fixture(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := Run(ctx, runner, opts, nil)
	if !errors.Is(err, context.Canceled) || result.Evidence.Outcome.Status != report.Interrupted {
		t.Fatalf("result %+v error %v", result, err)
	}
	assertEvidence(t, result)
	opts.Checks[0].Kind = "unknown"
	result, err = Run(t.Context(), runner, opts, nil)
	if err == nil {
		t.Fatal("invalid check accepted")
	}
	assertEvidence(t, result)
}
