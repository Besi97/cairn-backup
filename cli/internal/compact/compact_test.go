package compact

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	zero := 0
	for _, tt := range []struct {
		name    string
		options Options
		want    []string
		ssh     bool
	}{
		{"local", Options{Repository: "repo with spaces", PassphraseFile: "secret"},
			[]string{"compact", "--lock-wait", "60", "--verbose", "--progress", "--", filepath.Join(dir, "repo with spaces")}, false},
		{"SSH", Options{Repository: "user@[::1]:repo", IdentityFile: "identity", LockWait: &zero},
			[]string{"compact", "--lock-wait", "0", "--verbose", "--progress", "--", "user@[::1]:repo"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Prepare(tt.options, dir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Args, tt.want) || got.Dir != dir || got.Access.SSH != tt.ssh {
				t.Fatalf("request = %+v, want args %q, SSH %v", got, tt.want, tt.ssh)
			}
			if tt.options.PassphraseFile != "" && got.Access.PassphraseFile != filepath.Join(dir, "secret") {
				t.Fatalf("passphrase file = %q", got.Access.PassphraseFile)
			}
			if tt.ssh && got.Access.IdentityFile != filepath.Join(dir, "identity") {
				t.Fatalf("identity file = %q", got.Access.IdentityFile)
			}
		})
	}
}

func TestInvalidOptions(t *testing.T) {
	negative := -1
	for _, tt := range []struct {
		options Options
		want    string
	}{
		{Options{}, "repository"},
		{Options{Repository: "repo::archive"}, "not an archive"},
		{Options{Repository: "repo", IdentityFile: "identity"}, "SSH"},
		{Options{Repository: "repo", LockWait: &negative}, "nonnegative"},
	} {
		if _, err := Prepare(tt.options, t.TempDir()); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("options %+v: error = %v, want %q", tt.options, err, tt.want)
		}
	}
	if _, err := Prepare(Options{Repository: "repo"}, "relative"); err == nil {
		t.Fatal("accepted relative invocation directory")
	}
}
