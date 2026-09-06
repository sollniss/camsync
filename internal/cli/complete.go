package cli

import (
	"context"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/sollniss/camsync/app"
)

// completeDirs completes the single positional DIR argument with directories.
func completeDirs(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// completeCamera offers the cameras found in DIR (or the current directory)
// as values for --camera: the name and the row number "list" would print.
// The configuration is not loaded during completion, so defaults apply.
func completeCamera(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	res, err := app.ListHandler{}.Handle(context.Background(), app.ListQuery{Scope: app.Scope{Dir: dir}})
	if err != nil {
		cobra.CompErrorln(err.Error())
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, c := range res.Cameras {
		files := strconv.Itoa(len(c.Photos)) + " files"
		out = append(out, cobra.CompletionWithDesc(c.Name, files))
		out = append(out, cobra.CompletionWithDesc(strconv.Itoa(c.Index), c.Name+", "+files))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// registerCompletions wires value completion for the flags of cmd. Flags not
// listed keep cobra's default behaviour.
func registerCompletions(cmd *cobra.Command, flags map[string]cobra.CompletionFunc) {
	for name, fn := range flags {
		if err := cmd.RegisterFlagCompletionFunc(name, fn); err != nil {
			panic(err) // programmer error: the flag was not defined
		}
	}
}

// noFileComp completes nothing but suppresses file names, for free-form
// values such as durations.
func noFileComp(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}
