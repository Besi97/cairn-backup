package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	zero := 0
	options := Options{Repository: "repo with spaces", PassphraseFile: "secret", LockWait: &zero}
	for _, inspect := range []bool{false, true} {
		var request borg.Request
		var err error
		args := []string{"list", "--lock-wait", "0"}
		if inspect {
			request, err = PrepareInspect(options, dir, "-before {hostname}")
			args = append(args, "--json-lines", "--format", "{size}{mtime}", "--", filepath.Join(dir, options.Repository)+"::-before {hostname}")
		} else {
			request, err = PrepareList(options, dir)
			args = append(args, "--json", "--consider-checkpoints", "--", filepath.Join(dir, options.Repository))
		}
		if err != nil || !reflect.DeepEqual(request.Args, args) || request.Dir != dir ||
			request.Access.PassphraseFile != filepath.Join(dir, "secret") {
			t.Fatalf("request = %+v, error = %v", request, err)
		}
	}
	remote, err := PrepareInspect(Options{Repository: "ssh://user@host/./repo", IdentityFile: "key"}, dir, "archive")
	if err != nil || !remote.Access.SSH || remote.Access.IdentityFile != filepath.Join(dir, "key") {
		t.Fatalf("remote request = %+v, error = %v", remote, err)
	}
}

func TestPrepareInvalid(t *testing.T) {
	dir := t.TempDir()
	negative := -1
	for _, options := range []Options{
		{}, {Repository: "repo::archive"}, {Repository: "repo", IdentityFile: "key"}, {Repository: "repo", LockWait: &negative},
	} {
		if _, err := PrepareList(options, dir); err == nil {
			t.Fatalf("accepted %+v", options)
		}
		if _, err := PrepareInspect(options, dir, "archive"); err == nil {
			t.Fatalf("accepted %+v", options)
		}
	}
	if _, err := PrepareInspect(Options{Repository: "repo"}, dir, ""); err == nil {
		t.Fatal("accepted missing archive")
	}
}

func TestServiceStreaming(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "borg")
	if out, err := exec.Command("go", "build", "-o", binary, "./testdata/listborg").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tt := range []struct {
		name      string
		data      string
		exit      string
		wait      bool
		callback  error
		want      borg.Outcome
		wantError bool
	}{
		{name: "success", data: `{"archives":[` + archiveJSON + `]}`, want: borg.Success},
		{name: "warning", data: `{"archives":[]}`, exit: "100", want: borg.Warning},
		{name: "failure", exit: "2", want: borg.Failure, wantError: true},
		{name: "malformed", data: `{"archives":false}`, wait: true, want: borg.Failure, wantError: true},
		{name: "callback", data: `{"archives":[` + archiveJSON + `]}`, wait: true, callback: errors.New("consumer failed"), want: borg.Failure, wantError: true},
		{name: "callback EOF", data: `{"archives":[` + archiveJSON + `]}`, wait: true, callback: io.EOF, want: borg.Failure, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dataPath := filepath.Join(t.TempDir(), "data")
			if err := os.WriteFile(dataPath, []byte(tt.data), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			request := borg.Request{Args: []string{"list"}, Env: map[string]string{"LIST_BORG_DATA": dataPath, "LIST_BORG_EXIT": tt.exit}}
			if tt.wait {
				request.Env["LIST_BORG_WAIT"] = "yes"
			}
			var diagnostics bytes.Buffer
			request.Diagnostics = &diagnostics
			result, err := List(ctx, borg.Runner{Executable: binary}, request, func(Archive) error { return tt.callback })
			if result.Outcome != tt.want || (err != nil) != tt.wantError || ctx.Err() != nil {
				t.Fatalf("result = %+v, error = %v, context = %v", result, err, ctx.Err())
			}
			if tt.callback != nil && !errors.Is(err, tt.callback) {
				t.Fatalf("lost callback error: %v", err)
			}
			if diagnostics.String() != "listing diagnostic\n" {
				t.Fatalf("diagnostics = %q", &diagnostics)
			}
		})
	}
	t.Run("cancel after first file", func(t *testing.T) {
		dataPath := filepath.Join(t.TempDir(), "data")
		if err := os.WriteFile(dataPath, []byte(fileJSON+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		request := borg.Request{Args: []string{"list"}, Env: map[string]string{"LIST_BORG_DATA": dataPath, "LIST_BORG_WAIT": "yes"}}
		count := 0
		result, err := Inspect(ctx, borg.Runner{Executable: binary}, request, func(File) error {
			count++
			cancel()
			return nil
		})
		if count != 1 || result.Outcome != borg.Interrupted || !errors.Is(err, context.Canceled) {
			t.Fatalf("count = %d, result = %+v, error = %v", count, result, err)
		}
	})
	t.Run("cancel with partial file record", func(t *testing.T) {
		dataPath := filepath.Join(t.TempDir(), "data")
		if err := os.WriteFile(dataPath, []byte(fileJSON+"\n"+`{"path":`), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result, err := Inspect(ctx, borg.Runner{Executable: binary},
			borg.Request{Args: []string{"list"}, Env: map[string]string{"LIST_BORG_DATA": dataPath, "LIST_BORG_WAIT": "yes"}},
			func(File) error { cancel(); return nil })
		var secondary *borg.SecondaryError
		if result.Outcome != borg.Interrupted || !borg.IsCancellation(err) || !errors.Is(err, context.Canceled) ||
			!errors.As(err, &secondary) || !bytes.Contains([]byte(secondary.Error()), []byte("invalid JSON")) {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	})
}
