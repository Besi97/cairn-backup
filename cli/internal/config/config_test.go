package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func testEnvironment(home string, values map[string]string, system string) Environment {
	if values == nil {
		values = map[string]string{}
	}
	return Environment{
		Getenv:           func(key string) string { return values[key] },
		UserHomeDir:      func() (string, error) { return home, nil },
		SystemConfigPath: system,
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const sampleConfig = `version: 1
repositories:
  local:
    location: backups/local
    passphrase_file: secrets/local.passphrase
  offsite:
    location: ssh://backup@example.invalid/srv/cairn
    identity_file: keys/offsite
    lock_wait: 5m
jobs:
  service:
    repository: local
    source_root: data
    sources:
      - service/a
      - service/b
    archive_prefix: service
`

func TestDiscoverPrecedence(t *testing.T) {
	root := t.TempDir()
	explicit := filepath.Join(root, "explicit.yaml")
	user := filepath.Join(root, "xdg", "cairn", "config.yaml")
	system := filepath.Join(root, "system.yaml")
	for _, path := range []string{explicit, user, system} {
		writeFile(t, path, "version: 1\n")
	}

	environment := testEnvironment("/home/tester", map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "xdg")}, system)

	for _, test := range []struct {
		name     string
		explicit string
		want     string
	}{
		{name: "explicit wins", explicit: explicit, want: explicit},
		{name: "user when no explicit", want: user},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := environment.Discover(test.explicit)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if got != test.want {
				t.Errorf("Discover = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDiscoverFallsBackToSystem(t *testing.T) {
	root := t.TempDir()
	system := filepath.Join(root, "system.yaml")
	writeFile(t, system, "version: 1\n")

	environment := testEnvironment(root, nil, system)
	got, err := environment.Discover("")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != system {
		t.Errorf("Discover = %q, want %q", got, system)
	}
}

func TestDiscoverExplicitMissingDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	system := filepath.Join(root, "system.yaml")
	writeFile(t, system, "version: 1\n")

	environment := testEnvironment(root, nil, system)
	_, err := environment.Discover(filepath.Join(root, "absent.yaml"))
	if err == nil {
		t.Fatal("Discover succeeded, want error for a missing explicit configuration")
	}
}

func TestDiscoverBrokenUserDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	xdg := filepath.Join(root, "xdg")
	user := filepath.Join(xdg, "cairn", "config.yaml")
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatalf("create user configuration directory: %v", err)
	}
	system := filepath.Join(root, "system.yaml")
	writeFile(t, system, "version: 1\n")

	environment := testEnvironment(root, map[string]string{"XDG_CONFIG_HOME": xdg}, system)
	_, err := environment.Discover("")
	if err == nil {
		t.Fatal("Discover succeeded, want error for an unreadable user configuration")
	}
}

func TestDiscoverNoConfiguration(t *testing.T) {
	root := t.TempDir()
	environment := testEnvironment(root, nil, filepath.Join(root, "system.yaml"))
	_, err := environment.Discover("")
	if err == nil {
		t.Fatal("Discover succeeded, want error when no configuration exists")
	}
}

func TestDiscoverRelativeXDGIsIgnored(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, ".config", "cairn", "config.yaml")
	writeFile(t, user, "version: 1\n")

	environment := testEnvironment(root, map[string]string{"XDG_CONFIG_HOME": "relative"}, filepath.Join(root, "system.yaml"))
	got, err := environment.Discover("")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != user {
		t.Errorf("Discover = %q, want %q", got, user)
	}
}

func TestLoadResolvesPathsFromConfigurationDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writeFile(t, path, sampleConfig)

	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	config, err := testEnvironment("/home/tester", nil, "").Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	local := config.Repositories["local"]
	if got, want := local.Location, filepath.Join(root, "backups", "local"); got != want {
		t.Errorf("local location = %q, want %q", got, want)
	}
	if got, want := local.PassphraseFile, filepath.Join(root, "secrets", "local.passphrase"); got != want {
		t.Errorf("passphrase file = %q, want %q", got, want)
	}
	if got, want := local.LockWait, "60s"; got != want {
		t.Errorf("lock_wait = %q, want default %q", got, want)
	}

	offsite := config.Repositories["offsite"]
	if got, want := offsite.Location, "ssh://backup@example.invalid/srv/cairn"; got != want {
		t.Errorf("offsite location = %q, want unchanged %q", got, want)
	}
	if got, want := offsite.IdentityFile, filepath.Join(root, "keys", "offsite"); got != want {
		t.Errorf("identity file = %q, want %q", got, want)
	}
	if got, want := offsite.LockWait, "5m"; got != want {
		t.Errorf("lock_wait = %q, want %q", got, want)
	}

	job := config.Jobs["service"]
	if got, want := job.SourceRoot, filepath.Join(root, "data"); got != want {
		t.Errorf("source_root = %q, want %q", got, want)
	}
	if got, want := job.Sources[0], "service/a"; got != want {
		t.Errorf("source = %q, want relative %q", got, want)
	}
	if got, want := job.SourcePaths()[0], filepath.Join(root, "data", "service", "a"); got != want {
		t.Errorf("source path = %q, want %q", got, want)
	}
	if got, want := config.Path(), path; got != want {
		t.Errorf("config path = %q, want %q", got, want)
	}
}

func TestLoadExpandsPaths(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writeFile(t, path, `version: 1
repositories:
  local:
    location: ~/repositories/local
    passphrase_file: $SECRET_DIR/passphrase
jobs:
  service:
    repository: local
    source_root: ${DATA_DIR}
    sources:
      - service
    archive_prefix: service
`)

	environment := testEnvironment(home, map[string]string{
		"SECRET_DIR": filepath.Join(root, "secrets"),
		"DATA_DIR":   filepath.Join(root, "data"),
	}, "")
	config, err := environment.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := config.Repositories["local"].Location, filepath.Join(home, "repositories", "local"); got != want {
		t.Errorf("location = %q, want %q", got, want)
	}
	if got, want := config.Repositories["local"].PassphraseFile, filepath.Join(root, "secrets", "passphrase"); got != want {
		t.Errorf("passphrase file = %q, want %q", got, want)
	}
	if got, want := config.Jobs["service"].SourceRoot, filepath.Join(root, "data"); got != want {
		t.Errorf("source_root = %q, want %q", got, want)
	}
}

func TestLoadDoesNotExpandSSHLocation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writeFile(t, path, `version: 1
repositories:
  offsite:
    location: ssh://backup@example.invalid/$DIR/cairn
jobs:
  service:
    repository: offsite
    source_root: data
    sources:
      - service
    archive_prefix: service
`)

	config, err := testEnvironment(root, nil, "").Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := config.Repositories["offsite"].Location, "ssh://backup@example.invalid/$DIR/cairn"; got != want {
		t.Errorf("location = %q, want %q", got, want)
	}
}

func TestClassifyLocation(t *testing.T) {
	for _, test := range []struct {
		location string
		want     LocationKind
	}{
		{location: "/srv/backups/cairn", want: LocationLocal},
		{location: "./backups/cairn", want: LocationLocal},
		{location: "backups/cairn", want: LocationLocal},
		{location: "ssh://backup@example.invalid/srv/cairn", want: LocationSSH},
		{location: "backup@example.invalid:srv/cairn", want: LocationSSH},
	} {
		if got := ClassifyLocation(test.location); got != test.want {
			t.Errorf("ClassifyLocation(%q) = %v, want %v", test.location, got, test.want)
		}
	}
}

func TestExpandPath(t *testing.T) {
	environment := testEnvironment("/home/tester", map[string]string{"DATA": "/srv/data"}, "")

	for _, test := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "plain", value: "/srv/backups", want: "/srv/backups"},
		{name: "home", value: "~/backups", want: "/home/tester/backups"},
		{name: "bare home", value: "~", want: "/home/tester"},
		{name: "variable", value: "$DATA/backups", want: "/srv/data/backups"},
		{name: "braced variable", value: "${DATA}/backups", want: "/srv/data/backups"},
		{name: "literal dollar", value: "cost$", want: "cost$"},
		{name: "user home untouched", value: "~other/backups", want: "~other/backups"},
		{name: "unset variable", value: "$MISSING/backups", wantErr: true},
		{name: "empty variable", value: "${DATA}/../$MISSING", wantErr: true},
		{name: "command substitution", value: "$(hostname)/backups", wantErr: true},
		{name: "unterminated brace", value: "${DATA/backups", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ExpandPath(test.value, environment)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ExpandPath(%q) succeeded, want error", test.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandPath(%q): %v", test.value, err)
			}
			if got != test.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestReadSecret(t *testing.T) {
	root := t.TempDir()

	for _, test := range []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{name: "plain", content: "correct horse battery staple", want: "correct horse battery staple"},
		{name: "trailing LF", content: "passphrase\n", want: "passphrase"},
		{name: "trailing CRLF", content: "passphrase\r\n", want: "passphrase"},
		{name: "preserves spaces", content: "  pass phrase  \n", want: "  pass phrase  "},
		{name: "preserves tabs", content: "\tpass\tphrase\n", want: "\tpass\tphrase"},
		{name: "empty", content: "", wantErr: true},
		{name: "only line break", content: "\n", wantErr: true},
		{name: "multiple lines", content: "first\nsecond\n", wantErr: true},
		{name: "embedded carriage return", content: "first\rsecond", wantErr: true},
		{name: "NUL byte", content: "pass\x00phrase", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-"))
			writeFile(t, path, test.content)

			got, err := ReadSecret(path)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ReadSecret succeeded, want error")
				}
				if strings.Contains(err.Error(), "correct horse") || strings.Contains(err.Error(), "pass") {
					t.Errorf("error leaked secret content: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadSecret: %v", err)
			}
			if got != test.want {
				t.Errorf("ReadSecret = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadSecretMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	_, err := ReadSecret(path)
	if err == nil {
		t.Fatal("ReadSecret succeeded, want error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not mention the path", err)
	}
}

func TestConfigReadPassphrase(t *testing.T) {
	root := t.TempDir()

	config := &Config{Repositories: map[string]*Repository{
		"plain": {},
		"secret": {
			PassphraseFile: filepath.Join(root, "passphrase"),
		},
	}}
	writeFile(t, filepath.Join(root, "passphrase"), "secret\n")

	if got, err := config.ReadPassphrase("plain"); err != nil || got != "" {
		t.Errorf("ReadPassphrase without a file = %q, %v; want empty and no error", got, err)
	}
	if got, err := config.ReadPassphrase("secret"); err != nil || got != "secret" {
		t.Errorf("ReadPassphrase = %q, %v; want %q and no error", got, err, "secret")
	}
	if _, err := config.ReadPassphrase("missing"); err == nil {
		t.Error("ReadPassphrase succeeded for an unknown repository")
	}
}

func TestConfigReadPassphraseErrorContext(t *testing.T) {
	config := &Config{Repositories: map[string]*Repository{
		"offsite": {PassphraseFile: filepath.Join(t.TempDir(), "absent")},
	}}

	_, err := config.ReadPassphrase("offsite")
	if err == nil {
		t.Fatal("ReadPassphrase succeeded, want error")
	}
	if !strings.Contains(err.Error(), `repository "offsite"`) || !strings.Contains(err.Error(), "passphrase_file") {
		t.Errorf("error %q lacks repository or field context", err)
	}
}

func TestDecodeRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "empty file", content: ""},
		{name: "unknown field", content: "version: 1\nunexpected: true\n"},
		{name: "duplicate key", content: "version: 1\nversion: 1\n"},
		{name: "multiple documents", content: "version: 1\n---\nversion: 1\n"},
		{name: "not a mapping", content: "- version\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, path, test.content)
			if _, err := testEnvironment("/home/tester", nil, "").Load(path); err == nil {
				t.Fatalf("Load succeeded, want error")
			}
		})
	}
}

func TestLoadExplicitPathExpands(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.yaml"), "version: 1\n")

	config, err := testEnvironment(home, nil, "").Load("~/config.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := config.Path(), filepath.Join(home, "config.yaml"); got != want {
		t.Errorf("config path = %q, want %q", got, want)
	}
}

func TestLoadSanitizesYAMLError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "version: supersecret-value\n")

	_, err := testEnvironment("/home/tester", nil, "").Load(path)
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	if strings.Contains(err.Error(), "supersecret-value") {
		t.Errorf("error leaked a configuration value: %v", err)
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error lacks line context: %v", err)
	}
}

func TestDiscoverDanglingSymlinkDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	xdg := filepath.Join(root, "xdg")
	user := filepath.Join(xdg, "cairn", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
		t.Fatalf("create user configuration directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing.yaml"), user); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	system := filepath.Join(root, "system.yaml")
	writeFile(t, system, "version: 1\n")

	environment := testEnvironment(root, map[string]string{"XDG_CONFIG_HOME": xdg}, system)
	if _, err := environment.Discover(""); err == nil {
		t.Fatal("Discover succeeded, want error for a dangling user configuration symlink")
	}
}

func TestDiscoverRejectsNonRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create FIFO: %v", err)
	}

	environment := testEnvironment("/home/tester", nil, "")
	if _, err := environment.Discover(path); err == nil {
		t.Fatal("Discover succeeded, want error for a non-regular file")
	}
}

func TestLoadPermissionDeniedConfiguration(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "version: 1\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := testEnvironment("/home/tester", nil, "").Load(path); err == nil {
		t.Fatal("Load succeeded, want a permission error")
	}
}

func TestReadSecretPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}

	path := filepath.Join(t.TempDir(), "passphrase")
	writeFile(t, path, "secret\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := ReadSecret(path); err == nil {
		t.Fatal("ReadSecret succeeded, want a permission error")
	}
}

func TestLoadMalformedExplicitDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	explicit := filepath.Join(root, "explicit.yaml")
	writeFile(t, explicit, "version: [\n")
	system := filepath.Join(root, "system.yaml")
	writeFile(t, system, "version: 1\n")

	environment := testEnvironment(root, nil, system)
	if _, err := environment.Load(explicit); err == nil {
		t.Fatal("Load succeeded, want an error for malformed explicit configuration")
	}
}

func TestLoadMalformedUserDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	xdg := filepath.Join(root, "xdg")
	user := filepath.Join(xdg, "cairn", "config.yaml")
	writeFile(t, user, "version: [\n")
	system := filepath.Join(root, "system.yaml")
	writeFile(t, system, "version: 1\n")

	environment := testEnvironment(root, map[string]string{"XDG_CONFIG_HOME": xdg}, system)
	if _, err := environment.Load(""); err == nil {
		t.Fatal("Load succeeded, want an error for malformed user configuration")
	}
}

func TestSecretsAreReadOnDemand(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writeFile(t, path, `version: 1
repositories:
  local:
    location: backups/local
    passphrase_file: passphrase
jobs:
  service:
    repository: local
    source_root: data
    sources:
      - service
    archive_prefix: service
`)

	config, err := testEnvironment(root, nil, "").Load(path)
	if err != nil {
		t.Fatalf("Load reads the secret: %v", err)
	}

	secretPath := filepath.Join(root, "passphrase")
	writeFile(t, secretPath, "first\n")
	if got, err := config.ReadPassphrase("local"); err != nil || got != "first" {
		t.Fatalf("ReadPassphrase = %q, %v; want %q", got, err, "first")
	}

	writeFile(t, secretPath, "second\n")
	if got, err := config.ReadPassphrase("local"); err != nil || got != "second" {
		t.Fatalf("ReadPassphrase after replacement = %q, %v; want %q", got, err, "second")
	}
}

func TestLoadDoesNotExpandSources(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writeFile(t, path, `version: 1
repositories:
  local:
    location: backups/local
jobs:
  service:
    repository: local
    source_root: data
    sources:
      - $DATA/service
    archive_prefix: service
`)

	config, err := testEnvironment(root, nil, "").Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	job := config.Jobs["service"]
	if got, want := job.Sources[0], "$DATA/service"; got != want {
		t.Errorf("source = %q, want unchanged %q", got, want)
	}
}

func TestLoadExampleConfiguration(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "config.example.yaml")
	config, err := testEnvironment("/home/tester", nil, "").Load(path)
	if err != nil {
		t.Fatalf("Load example configuration: %v", err)
	}
	if len(config.Repositories) == 0 || len(config.Jobs) == 0 {
		t.Fatal("example configuration loaded without repositories or jobs")
	}
}

func TestLoadPreservesExplicitEmptyLockWait(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writeFile(t, path, `version: 1
repositories:
  local:
    location: backups/local
    lock_wait: ""
`)

	config, err := testEnvironment(root, nil, "").Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := config.Repositories["local"].LockWait; got != "" {
		t.Errorf("lock_wait = %q, want empty so validation can reject it", got)
	}
}

func TestLoadRejectsEmptyOptionalPaths(t *testing.T) {
	for _, field := range []string{"passphrase_file", "identity_file"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, path, "version: 1\nrepositories:\n  local:\n    location: backups/local\n    "+field+": \"\"\n")

			if _, err := testEnvironment(t.TempDir(), nil, "").Load(path); err == nil {
				t.Fatalf("Load succeeded, want an error for empty %s", field)
			}
		})
	}
}

func TestLoadRejectsNullOptionalValues(t *testing.T) {
	for _, field := range []string{"passphrase_file", "identity_file", "lock_wait"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeFile(t, path, "version: 1\nrepositories:\n  local:\n    location: backups/local\n    "+field+": null\n")

			if _, err := testEnvironment(t.TempDir(), nil, "").Load(path); err == nil {
				t.Fatalf("Load succeeded, want an error for null %s", field)
			}
		})
	}
}
