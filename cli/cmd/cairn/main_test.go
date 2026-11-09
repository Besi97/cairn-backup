package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVersionOutput(t *testing.T) {
	for _, test := range []struct {
		name    string
		ldflags string
		want    string
	}{
		{name: "development", want: "cairn dev\n"},
		{name: "injected", ldflags: "-X main.version=0.1.0-test", want: "cairn 0.1.0-test\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "cairn")
			args := []string{"build", "-o", binary}
			if test.ldflags != "" {
				args = append(args, "-ldflags", test.ldflags)
			}
			args = append(args, ".")
			if output, err := exec.Command("go", args...).CombinedOutput(); err != nil {
				t.Fatalf("build CLI: %v\n%s", err, output)
			}

			output, err := exec.Command(binary, "--version").Output()
			if err != nil {
				t.Fatalf("run CLI: %v", err)
			}
			if got := string(output); got != test.want {
				t.Errorf("version output = %q, want %q", got, test.want)
			}
		})
	}
}
