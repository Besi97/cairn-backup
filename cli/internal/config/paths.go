package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/prune"
	"go.yaml.in/yaml/v3"
)

func localPath(value, base, home string) (string, error) {
	if value == "~" || strings.HasPrefix(value, "~/") {
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("cannot resolve home directory for local path")
		}
		if value == "~" {
			return home, nil
		}
		value = filepath.Join(home, value[2:])
	}
	if filepath.IsAbs(value) {
		return value, nil
	}
	return filepath.Join(base, value), nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (config *Config) resolve(home string, doc *yaml.Node) error {
	fail := func(fields []string, reason string) error {
		return diagnostic(config.Path, at(doc, fields), fields, reason)
	}
	for _, name := range sortedKeys(config.Repositories) {
		repo := config.Repositories[name]
		fields := []string{"repositories", name}
		// Use Borg's shared classifier before local home expansion. In
		// particular, host:~/repo belongs to the remote host, not this user.
		prepared, err := borg.PrepareRepository(borg.RepositoryOptions{
			Repository: repo.Location, IdentityFile: repo.IdentityFile,
		}, config.Directory)
		if err != nil {
			field, reason := "location", "invalid repository location; require a repository-only local path or SSH location with host and path"
			if repo.IdentityFile != "" {
				_, withoutIdentity := borg.PrepareRepository(borg.RepositoryOptions{Repository: repo.Location}, config.Directory)
				if withoutIdentity == nil {
					field, reason = "identity_file", "identity file requires an SSH repository"
				}
			}
			return fail(append(fields, field), reason)
		}
		if strings.ContainsRune(repo.Location, 0) {
			return fail(append(fields, "location"), "repository location must not contain NUL")
		}
		if !prepared.Access.SSH {
			value := strings.TrimPrefix(repo.Location, "file://")
			resolved, err := localPath(value, config.Directory, home)
			if err != nil {
				return fail(append(fields, "location"), err.Error())
			}
			if strings.HasPrefix(repo.Location, "file://") {
				resolved = "file://" + resolved
			}
			repo.Location = resolved
		}
		for _, entry := range []struct {
			field string
			value *string
		}{
			{"passphrase_file", &repo.PassphraseFile},
			{"identity_file", &repo.IdentityFile},
		} {
			if *entry.value == "" {
				continue
			}
			if strings.ContainsRune(*entry.value, 0) {
				return fail(append(fields, entry.field), "local path must not contain NUL")
			}
			resolved, err := localPath(*entry.value, config.Directory, home)
			if err != nil {
				return fail(append(fields, entry.field), err.Error())
			}
			*entry.value = resolved
		}
		config.Repositories[name] = repo
	}
	for _, name := range sortedKeys(config.Workflows) {
		workflow := config.Workflows[name]
		for si := range workflow.Stages {
			for ti := range workflow.Stages[si].Tasks {
				task := &workflow.Stages[si].Tasks[ti]
				fields := []string{"workflows", name, "stages", strconv.Itoa(si), "tasks", strconv.Itoa(ti)}
				repo, exists := config.Repositories[task.Repository]
				if !exists {
					return fail(append(fields, "repository"), "references an undefined repository")
				}
				defaultLockWait(task)
				if task.Type == "backup" {
					root := task.SourceRoot
					if root == "" {
						root = "."
					}
					if strings.ContainsRune(root, 0) {
						return fail(append(fields, "source_root"), "local path must not contain NUL")
					}
					resolved, err := localPath(root, config.Directory, home)
					if err != nil {
						return fail(append(fields, "source_root"), err.Error())
					}
					task.SourceRoot = resolved
					// Do not interpret placeholders or impose an identifier
					// alphabet. Expanded naming/collision checks belong to Borg.
					for _, entry := range []struct{ field, value string }{
						{"archive_prefix", task.ArchivePrefix}, {"archive_name", task.ArchiveName},
					} {
						if strings.ContainsAny(entry.value, "/\x00") || strings.Contains(entry.value, "::") {
							return fail(append(fields, entry.field), "archive names must not contain slash, NUL, or an archive separator")
						}
					}
				}
				if task.Type == "prune" {
					// Reuse native retention preparation without starting Borg
					// or opening access files. No interval duration conversion.
					_, err := prune.Prepare(prune.Options{
						Repository: repo.Location, ArchivePrefix: task.ArchivePrefix,
						LockWait: task.LockWait,
						Retention: prune.Retention{
							Within: task.KeepWithin, Secondly: task.KeepSecondly,
							Minutely: task.KeepMinutely, Hourly: task.KeepHourly, Daily: task.KeepDaily,
							Weekly: task.KeepWeekly, Monthly: task.KeepMonthly, Yearly: task.KeepYearly,
						},
					}, config.Directory)
					if err != nil {
						return fail(fields, "invalid prune scope or retention policy")
					}
				}
			}
		}
		config.Workflows[name] = workflow
	}
	return nil
}
