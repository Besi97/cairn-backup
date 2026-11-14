package configschema

import (
	"fmt"
	"testing"
)

func TestValidRepositories(t *testing.T) {
	schema := loadSchema(t)
	for _, definition := range []string{
		`{location: ./repo}`,
		`{location: /mnt/backups/borg}`,
		`{location: "../repo with spaces"}`,
		`{location: "~/backups/borg"}`,
		`{location: "file://./repo"}`,
		`{location: "ssh://backup@host:2222/./repo"}`,
		`{location: "backup@host:repo"}`,
		`{location: "backup@[::1]:repo"}`,
		`{location: ./repo, passphrase: "example-secret"}`,
		`{location: ./repo, passphrase: ""}`,
		`{location: ./repo, passphrase: "${LITERAL}~"}`,
		`{location: ./repo, passphrase_file: ./secrets/passphrase}`,
		`{location: ./repo, passphrase_file: "~/secrets/passphrase"}`,
		`{location: "backup@host:repo", identity_file: "~/.ssh/cairn"}`,
		`{location: "ssh://backup@host/./repo", passphrase: "example-secret", identity_file: ./identity}`,
		`{location: "ssh://backup@host/./repo", passphrase_file: ./secret, identity_file: ./identity}`,
	} {
		t.Run(definition, func(t *testing.T) {
			if err := schema.Validate(repositoryDocument(t, definition)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvalidRepositories(t *testing.T) {
	schema := loadSchema(t)
	for _, definition := range []string{
		`null`,
		`[]`,
		`"repo"`,
		`1`,
		`true`,
		`{}`,
		`{passphrase: secret}`,
		`{passphrase_file: ./secret}`,
		`{identity_file: ./identity}`,
		`{location: ""}`,
		`{location: ./repo, passphrase_file: ""}`,
		`{location: "backup@host:repo", identity_file: ""}`,
		`{location: ./repo, passphrase: secret, passphrase_file: ./secret}`,
		`{location: ./repo, passphrase: "", passphrase_file: ./secret}`,
		`{location: ./repo, unknown: true}`,
		`{location: ./repo, repository: other}`,
		`{location: ./repo, type: backup}`,
		`{location: ./repo, archive_prefix: documents}`,
		`{location: ./repo, sources: [.]}`,
		`{location: ./repo, keep_daily: 7}`,
		`{location: ./repo, lock_wait: 60}`,
	} {
		t.Run(definition, func(t *testing.T) {
			if err := schema.Validate(repositoryDocument(t, definition)); err == nil {
				t.Fatal("schema accepted invalid repository")
			}
		})
	}
	for _, field := range []string{"location", "passphrase", "passphrase_file", "identity_file"} {
		for _, value := range []string{"null", "123", "1.5", "true", "[]", "{}"} {
			t.Run(fmt.Sprintf("%s: %s", field, value), func(t *testing.T) {
				doc := baseDocument(t)
				repository(doc)[field] = yamlValue(t, value)
				if err := schema.Validate(doc); err == nil {
					t.Fatal("schema accepted non-string repository field")
				}
			})
		}
	}
}

func TestRepositoryNames(t *testing.T) {
	schema := loadSchema(t)
	doc := baseDocument(t)
	// Multiple entries and non-lowercase names are allowed. Name resolution is
	// a runtime concern; the schema only requires nonempty string keys.
	for _, name := range []string{"Offsite", "offsite", "backup-2", "123", "backup server"} {
		doc["repositories"].(map[string]any)[name] = map[string]any{"location": "./repo"}
	}
	if err := schema.Validate(doc); err != nil {
		t.Fatal(err)
	}
}
