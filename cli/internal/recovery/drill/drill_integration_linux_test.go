package drill

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/assertions"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/report"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/selection"
)

func TestRealBorgDrill(t *testing.T) {
	binary := os.Getenv("CAIRN_TEST_BORG")
	if binary == "" {
		t.Skip("set CAIRN_TEST_BORG to exercise real Borg")
	}
	base := t.TempDir()
	t.Setenv("BORG_BASE_DIR", filepath.Join(base, "borg"))
	t.Setenv("BORG_CACHE_DIR", filepath.Join(base, "cache"))
	t.Setenv("BORG_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("BORG_SECURITY_DIR", filepath.Join(base, "security"))
	t.Setenv("BORG_PASSPHRASE", "incorrect-inherited-secret")
	secret := "drill-test-key"
	repo := borg.PreparedRepository{Location: filepath.Join(base, "repo"), Access: borg.AccessOptions{Passphrase: &secret}}
	runner := borg.Runner{Executable: binary}
	if err := os.WriteFile(filepath.Join(base, "file"), []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--encryption=repokey", repo.Location}, {"create", repo.Location + "::snapshot", "file"}} {
		var output bytes.Buffer
		r, err := runner.Run(t.Context(), borg.Request{Args: args, Dir: base, Access: repo.Access, Diagnostics: &output})
		if err != nil || r.Outcome != borg.Success {
			t.Fatalf("%v: %+v %v: %s", args, r, err, output.String())
		}
	}
	var diagnostics bytes.Buffer
	result, err := Run(t.Context(), runner, Options{
		Repository: repo, Dir: base, Selector: selection.Selector{Exact: "snapshot"}, Paths: []string{"file"},
		Checks: []assertions.Check{{Kind: assertions.FileBytes, Path: "file", Bytes: []byte("contents")}},
		Sample: &SampleOptions{LiveRoot: base, Limit: 1},
	}, &diagnostics)
	if err != nil || result.Warning || result.BorgCheck.Status != report.Passed || result.Evidence.Outcome.Status != report.Passed {
		t.Fatalf("result %+v error %v: %s", result, err, diagnostics.String())
	}
	assertEvidence(t, result)
}
