package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

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
		if strings.ContainsAny(toComplete, ":@") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveDefault
	})
}
