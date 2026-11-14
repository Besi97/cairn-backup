package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var scpRepositoryPrefix = regexp.MustCompile(`^(?:[^@:/]+@)?(?:\[[0-9a-fA-F:.]+\]|[^:/]+):`)

func registerFileCompletion(cmd *cobra.Command, name string) {
	_ = cmd.MarkFlagFilename(name)
}

func registerDirectoryCompletion(cmd *cobra.Command, name string) {
	_ = cmd.MarkFlagDirname(name)
}

func registerNoFileCompletion(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		_ = cmd.RegisterFlagCompletionFunc(name, cobra.NoFileCompletions)
	}
}

func registerRepositoryCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("repository", func(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		// Let the shell complete local repository paths, but do not offer local
		// filenames after the user has started an SSH/scp-style location.
		if strings.HasPrefix(toComplete, "ssh://") ||
			(!strings.HasPrefix(toComplete, "file://") && scpRepositoryPrefix.MatchString(toComplete)) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveDefault
	})
}

func completeBackupSources(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	sourceRoot, _ := cmd.Flags().GetString("source-root")
	if sourceRoot == "" || filepath.IsAbs(toComplete) {
		return nil, cobra.ShellCompDirectiveDefault
	}

	invocationDir, err := os.Getwd()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if !filepath.IsAbs(sourceRoot) {
		sourceRoot = filepath.Join(invocationDir, sourceRoot)
	}

	dir, prefix := filepath.Split(toComplete)
	if strings.HasSuffix(toComplete, string(filepath.Separator)) {
		dir, prefix = toComplete, ""
	}
	lookupDir := appendPathPreservingComponents(sourceRoot, dir)
	entries, err := os.ReadDir(lookupDir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	completions := make([]cobra.Completion, 0, len(entries))
	directive := cobra.ShellCompDirectiveNoFileComp
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		name = dir + name
		info, err := os.Stat(appendPathPreservingComponents(lookupDir, entry.Name()))
		if err == nil && info.IsDir() {
			name += string(filepath.Separator)
			directive |= cobra.ShellCompDirectiveNoSpace
		}
		completions = append(completions, cobra.Completion(name))
	}
	return completions, directive
}

func appendPathPreservingComponents(base, suffix string) string {
	if suffix == "" {
		return base
	}
	if strings.HasSuffix(base, string(filepath.Separator)) {
		return base + suffix
	}
	return base + string(filepath.Separator) + suffix
}
