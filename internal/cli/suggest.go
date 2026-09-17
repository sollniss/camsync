package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/app"
)

func (rt *runtime) suggestCommand() *cobra.Command {
	var references []string
	var maxOffset string
	cmd := &cobra.Command{
		Use:   "suggest [DIR]",
		Short: "Estimate how far each camera's clock is off",
		Long: `Suggest measures every camera's clock against the clocks that can be
trusted and prints the shift that fixes it. Nothing is written; run the
printed "shift" commands to apply the suggestions.

Trusted (reference) clocks are, unless --reference names others:
  - phones, whose clocks are set from the network, and
  - cameras whose timestamps agree with the GPS time in their photos.

A camera with GPS times and a recorded time zone is measured against GPS
time directly. Every other camera is assumed to be off by the same amount
for all its photos, and that amount is found by lining up its shooting
activity with the reference photos: people photograph the same moments, so
the right shift makes the photos of both land close together. That needs
the cameras to have been at the same places for at least a few moments.
The result is usually right to within a minute; photos of the very same
moment make it more precise.

CONFIDENCE is high when no other shift comes close to explaining the photos
equally well, and low when the photos are too few or too evenly spread to
tell. --max-offset limits the search (default 12h); beyond half a day the
previous or next day's activity easily looks like a match.`,
		Example: `  camsync suggest ~/Photos/trip
  camsync suggest --reference "Canon EOS R5" ~/Photos/trip
  camsync suggest --max-offset 2h -o json ~/Photos/trip`,
		Args:              dirArgs,
		ValidArgsFunction: completeDirs,
		RunE: rt.runE(func(cmd *cobra.Command, args []string) error {
			q := app.SuggestQuery{Scope: rt.scope(args), References: references}
			if maxOffset != "" {
				d, err := camsync.ParseShift(maxOffset)
				if err != nil {
					return usage(err)
				}
				if d <= 0 {
					return usage(errors.New("--max-offset must be positive"))
				}
				q.MaxOffset = d
			}
			res, err := app.SuggestHandler{}.Handle(cmd.Context(), q)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if rt.json() {
				return writeJSON(out, suggestView(res))
			}
			return printSuggest(out, res, shiftFlags(q.Scope))
		}),
	}
	f := cmd.Flags()
	f.StringArrayVar(&references, "reference", nil, `trusted camera as printed by "list"; repeat for several (default: phones and cameras that agree with GPS)`)
	f.StringVar(&maxOffset, "max-offset", "", "largest clock offset to search for, e.g. 30m or 14h (default 12h)")
	registerCompletions(cmd, map[string]cobra.CompletionFunc{"reference": completeCamera, "max-offset": noFileComp})
	return cmd
}

// shiftFlags renders the settings a printed "shift" command needs to act on
// the same photos as this invocation, followed by the directory.
func shiftFlags(scope app.Scope) []string {
	var flags []string
	if scope.Recursive {
		flags = append(flags, "-r")
	}
	if scope.SidecarStyle == camsync.SidecarByName {
		flags = append(flags, "--sidecar-style", "name")
	}
	return append(flags, scope.Dir)
}
