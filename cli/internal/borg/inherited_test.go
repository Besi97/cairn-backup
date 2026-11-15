package borg

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInheritedLocksPreserveCredentialDescriptor(t *testing.T) {
	isolateCredentials(t)
	base := t.TempDir()
	secretPath := filepath.Join(base, "passphrase")
	const secret = "literal {hostname} $HOME"
	if err := os.WriteFile(secretPath, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	var files []*os.File
	for range 2 {
		file, err := os.CreateTemp(base, "lock")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		files = append(files, file)
	}
	req := request()
	req.Access.PassphraseFile = secretPath
	req.Env["FAKE_CREDENTIAL_PATH"] = secretPath
	req.Env["FAKE_READ_PASSPHRASE"] = "yes"
	var output bytes.Buffer
	req.Diagnostics = &output
	result, err := (Runner{Executable: fakeBorg, InheritedFiles: files}).Run(t.Context(), req)
	if err != nil || result.Outcome != Success ||
		!strings.Contains(output.String(), fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))) {
		t.Fatalf("credential with two lock descriptors: %+v, %v\n%s", result, err, &output)
	}
	for _, file := range files {
		if _, err := file.Stat(); err != nil {
			t.Fatal("runner closed caller-owned lock:", err)
		}
	}
}
