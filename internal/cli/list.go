package cli

import (
	"github.com/spf13/cobra"

	"github.com/sollniss/camsync/app"
)

func (rt *runtime) listCommand() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:     "list [DIR]",
		Aliases: []string{"ls"},
		Short:   "Show the cameras in a directory and the shift applied to each",
		Long: `List scans DIR (default ".") and prints one row per camera: how many
photos it took, their date range, the shift currently applied through
sidecars and anything irregular. The row number can be used as --camera.`,
		Example: `  camsync list ~/Photos/trip
  camsync list -r -v ~/Photos
  camsync list -o json ~/Photos/trip | jq '.cameras[].camera'`,
		Args:              dirArgs,
		ValidArgsFunction: completeDirs,
		RunE: rt.runE(func(cmd *cobra.Command, args []string) error {
			res, err := app.ListHandler{}.Handle(cmd.Context(), app.ListQuery{Scope: rt.scope(args)})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if rt.json() {
				return writeJSON(out, listView(res))
			}
			return printList(out, res, verbose)
		}),
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also list every file")
	return cmd
}
