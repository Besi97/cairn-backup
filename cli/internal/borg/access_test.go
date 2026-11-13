package borg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func envValues(env []string) map[string]string {
	values := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

func TestCredentialPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(path, []byte("file secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		file string
		env  []string
		want map[string]string
	}{
		{"file", path, []string{"BORG_PASSPHRASE=ignored", "BORG_PASSCOMMAND=ignored", "BORG_PASSPHRASE_FD=invalid"}, map[string]string{"BORG_PASSPHRASE_FD": "3"}},
		{"environment", "", []string{"BORG_PASSPHRASE=secret", "BORG_PASSCOMMAND=ignored", "BORG_PASSPHRASE_FD=invalid"}, map[string]string{"BORG_PASSPHRASE": "secret", "BORG_PASSCOMMAND": "ignored"}},
		{"empty environment", "", []string{"BORG_PASSPHRASE=", "BORG_PASSPHRASE_FD=invalid"}, map[string]string{"BORG_PASSPHRASE": ""}},
		{"command", "", []string{"BORG_PASSCOMMAND=command", "BORG_PASSPHRASE_FD=invalid"}, map[string]string{"BORG_PASSCOMMAND": "command"}},
		{"empty command", "", []string{"BORG_PASSCOMMAND=", "BORG_PASSPHRASE_FD=invalid"}, map[string]string{"BORG_PASSCOMMAND": ""}},
		{"no credentials", "", nil, map[string]string{"BORG_PASSPHRASE": ""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, err := prepareAccess(AccessOptions{PassphraseFile: tt.file}, tt.env)
			if err != nil {
				t.Fatal(err)
			}
			defer a.close()
			got := envValues(a.env)
			for _, key := range []string{"BORG_PASSPHRASE", "BORG_PASSCOMMAND", "BORG_PASSPHRASE_FD"} {
				value, present := got[key]
				want, wantPresent := tt.want[key]
				if present != wantPresent || value != want {
					t.Errorf("incorrect presence or value for %s", key)
				}
			}
		})
	}
}

func TestPassphraseForwarding(t *testing.T) {
	isolateCredentials(t)
	for _, inherited := range []bool{false, true} {
		t.Run(fmt.Sprintf("inherited=%v", inherited), func(t *testing.T) {
			secret := []byte("secret with spaces\n\n")
			path := filepath.Join(t.TempDir(), "passphrase")
			if err := os.WriteFile(path, secret, 0600); err != nil {
				t.Fatal(err)
			}
			req := request()
			req.Dir = t.TempDir()
			req.Env["FAKE_READ_PASSPHRASE"] = "yes"
			req.Env["FAKE_CREDENTIAL_PATH"] = path
			var callerFile *os.File
			if inherited {
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				callerFile = file
				// Match a shell-provided inherited descriptor, not Go's
				// usual close-on-exec file handles.
				if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), syscall.F_SETFD, 0); errno != 0 {
					t.Fatal(errno)
				}
				req.Env["BORG_PASSPHRASE_FD"] = strconv.FormatUint(uint64(file.Fd()), 10)
			} else {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				req.Access.PassphraseFile, err = filepath.Rel(cwd, path)
				if err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			req.Diagnostics = &output
			result, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req)
			if err != nil || result.Outcome != Success {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if !strings.Contains(output.String(), fmt.Sprintf("%x", sha256.Sum256(secret))) {
				t.Fatal("descriptor did not reach operation intact")
			}
			if strings.Contains(output.String(), string(secret)) {
				t.Fatal("secret leaked into diagnostics")
			}
			if callerFile != nil {
				if _, err := callerFile.Stat(); err != nil {
					t.Error("caller descriptor was closed")
				}
			}
		})
	}
}

func TestPipePassphrase(t *testing.T) {
	isolateCredentials(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	secret := []byte("pipe secret\n")
	if _, err := writer.Write(secret); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := request()
	req.Env["BORG_PASSPHRASE_FD"] = strconv.FormatUint(uint64(reader.Fd()), 10)
	req.Env["FAKE_READ_PASSPHRASE"] = "yes"
	var output bytes.Buffer
	req.Diagnostics = &output
	if _, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), fmt.Sprintf("%x", sha256.Sum256(secret))) {
		t.Fatal("pipe contents did not reach Borg")
	}
}

func TestInvalidAccess(t *testing.T) {
	writeOnly, err := os.OpenFile(filepath.Join(t.TempDir(), "output"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writeOnly.Close()
	for _, fd := range []string{"not-a-descriptor", "-1", "99999999", "4294967296", strconv.FormatUint(uint64(writeOnly.Fd()), 10)} {
		if _, err := prepareAccess(AccessOptions{}, []string{"BORG_PASSPHRASE_FD=" + fd}); err == nil || strings.Contains(err.Error(), fd) {
			t.Error("expected descriptor error without echoing the input")
		}
	}
	for _, options := range []AccessOptions{
		{PassphraseFile: filepath.Join(t.TempDir(), "missing")},
		{PassphraseFile: t.TempDir()},
		{IdentityFile: "identity"},
	} {
		if _, err := prepareAccess(options, nil); err == nil {
			t.Error("accepted invalid access options")
		}
	}
}

func TestAccessCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := prepareAccess(AccessOptions{PassphraseFile: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.close()
	if _, err := a.files[0].Stat(); err == nil {
		t.Error("owned descriptor remains open")
	}
	failed, err := prepareAccess(AccessOptions{PassphraseFile: path, IdentityFile: "invalid-for-local"}, nil)
	if err == nil {
		t.Fatal("expected preparation failure")
	}
	if _, err := failed.files[0].Stat(); err == nil {
		t.Error("descriptor remains open after preparation failure")
	}
}

func TestCanceledAccessCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := request()
	req.Access.PassphraseFile = path
	req.Diagnostics = cancelWriter{cancel}
	result, err := (Runner{Executable: fakeBorg}).Run(ctx, req)
	if err == nil || result.Outcome != Interrupted {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		if target, _ := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); target == path {
			t.Fatal("owned passphrase file remains open after cancellation")
		}
	}
}

func TestSSHAccess(t *testing.T) {
	identity := "identity with 'quotes' and $(false); *"
	a, err := prepareAccess(AccessOptions{SSH: true, IdentityFile: identity}, []string{"BORG_RSH=unsafe", "SSH_AUTH_SOCK=agent"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	path, err := filepath.Abs(identity)
	if err != nil {
		t.Fatal(err)
	}
	want := "ssh -oBatchMode=yes -oStrictHostKeyChecking=yes -i '" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
	if a.rsh != want {
		t.Error("unexpected SSH invocation")
	}
	if _, ok := envValues(a.env)["BORG_RSH"]; ok {
		t.Error("inherited transport override retained")
	}
	if envValues(a.env)["SSH_AUTH_SOCK"] != "agent" {
		t.Error("SSH agent not preserved")
	}
	local, err := prepareAccess(AccessOptions{}, nil)
	if err != nil || local.rsh != "" {
		t.Fatal("local access requires SSH")
	}
}
