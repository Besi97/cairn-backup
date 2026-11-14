package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRepositoryPaths(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"./repo", "BASE/repo"},
		{"../repo", "PARENT/repo"},
		{"/absolute/repo", "/absolute/repo"},
		{"~/repo", "HOME/repo"},
		{"~", "HOME"},
		{"~another/repo", "BASE/~another/repo"},
		{"$LITERAL/repo", "BASE/$LITERAL/repo"},
		{"file://./repo", "file://BASE/repo"},
		{"file://~/repo", "file://HOME/repo"},
		{"ssh://backup@host/~/repo", "ssh://backup@host/~/repo"},
		{"backup@host:~/repo", "backup@host:~/repo"},
		{"backup@[::1]:repo", "backup@[::1]:repo"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			options := testOptions(t)
			options.Explicit, options.Path = true, "sub/config.yaml"
			base := filepath.Join(options.Directory, "sub")
			writeConfig(t, filepath.Join(options.Directory, options.Path),
				strings.Replace(withTask("{type: compact, repository: local}"), "./repo", "'"+tt.input+"'", 1))
			config, err := Load(options)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.NewReplacer("BASE", base, "PARENT", options.Directory, "HOME", options.Home).Replace(tt.want)
			if got := config.Repositories["local"].Location; got != want {
				t.Fatalf("location = %q, want %q", got, want)
			}
		})
	}
}

func TestPathsAndNativeArguments(t *testing.T) {
	text := `version: 1
repositories:
  local:
    location: ssh://backup@host/./repo
    passphrase_file: ./missing-secret
    identity_file: ~/.ssh/missing-key
workflows:
  manual:
    stages:
      - tasks:
          - type: backup
            repository: local
            archive_prefix: "{hostname}-documents"
            source_root: ~/missing-source
            sources: [".", "../other", "/absolute", "~/literal", "a/../b", "$LITERAL"]
            exclude: ["*.tmp", "re:cache", ""]
`
	config, err := loadText(t, text)
	if err != nil {
		t.Fatal(err)
	}
	base := config.Directory
	repo := config.Repositories["local"]
	task := firstTask(config)
	if repo.PassphraseFile != filepath.Join(base, "missing-secret") ||
		repo.IdentityFile != filepath.Join(base, "home/.ssh/missing-key") ||
		task.SourceRoot != filepath.Join(base, "home/missing-source") {
		t.Fatalf("paths: secret=%q identity=%q root=%q", repo.PassphraseFile, repo.IdentityFile, task.SourceRoot)
	}
	if task.ArchivePrefix != "{hostname}-documents" ||
		!reflect.DeepEqual(task.Sources, []string{".", "../other", "/absolute", "~/literal", "a/../b", "$LITERAL"}) ||
		!reflect.DeepEqual(task.Exclude, []string{"*.tmp", "re:cache", ""}) {
		t.Fatal("native Borg arguments changed")
	}
	if *task.LockWait != 60 {
		t.Fatal("wrong default lock wait")
	}
}

func TestDefaultRootAndCredentialPresence(t *testing.T) {
	for _, secret := range []string{"", ", passphrase: ''", ", passphrase: '${LITERAL}~'"} {
		text := strings.Replace(withTask("{type: backup, repository: local, archive_name: '{hostname}-exact', sources: [.]}"),
			"{location: ./repo}", "{location: ./repo"+secret+"}", 1)
		config, err := loadText(t, text)
		if err != nil {
			t.Fatal(err)
		}
		if firstTask(config).SourceRoot != config.Directory {
			t.Fatal("default root is not the config directory")
		}
		value := config.Repositories["local"].Passphrase
		if (value == nil) != (secret == "") {
			t.Fatal("credential field presence lost")
		}
		if value != nil && strings.Contains(secret, "LITERAL") && *value != "${LITERAL}~" {
			t.Fatal("inline secret expanded")
		}
	}
}

func TestMissingHomeOnlyFailsWhenNeeded(t *testing.T) {
	options := testOptions(t)
	options.Home, options.Explicit, options.Path = "", true, "config.yaml"
	path := filepath.Join(options.Directory, options.Path)
	writeConfig(t, path, minimal)
	if _, err := Load(options); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, strings.Replace(withTask("{type: compact, repository: local}"), "./repo", "'~/repo'", 1))
	if _, err := Load(options); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestSelectedSymlinkDirectory(t *testing.T) {
	options := testOptions(t)
	options.Explicit, options.Path = true, "selected/config.yaml"
	target := filepath.Join(options.Directory, "target/config.yaml")
	writeConfig(t, target, withTask("{type: compact, repository: local}"))
	selected := filepath.Join(options.Directory, options.Path)
	if err := os.MkdirAll(filepath.Dir(selected), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, selected); err != nil {
		t.Fatal(err)
	}
	config, err := Load(options)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Repositories["local"].Location; got != filepath.Join(filepath.Dir(selected), "repo") {
		t.Fatalf("location = %q", got)
	}
}
