package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sollniss/camsync/app"
)

func (rt *runtime) localizeCommand() *cobra.Command {
	var clock clockFlags
	var camera, zone string
	var dryRun, verbose bool
	cmd := &cobra.Command{
		Use:   "localize [DIR]",
		Short: "Rewrite timestamps in the local time of where each photo was taken",
		Long: `Localize rewrites every photo's timestamp in the time zone of the place it
was taken, keeping the moment. A photo from a camera left on Japan time,
taken at 21:18 +09:00 in Germany in summer, becomes 14:18 +02:00. Zone-aware
readers such as Immich then show and group the trip by local dates instead
of the camera's zone or the browser's.

The local zone comes from phones, which follow it wherever they are: the zone
their photos record, or the one implied by their GPS time. For every photo
the phone photo closest in time decides, within --max-gap (default 24h), so
trips across zones and daylight saving changes come out right. Where no phone
photo is near, --zone sets the local zone; otherwise the photo is skipped.

To know the moment a photo was taken, every camera's clock zone must be
known; see "camsync suggest --help". Check it with suggest first and fix a
wrong guess with --clock-zone. Fix clock errors with shift before localizing:
the local zone is looked up at the moment the camera's clock says.

Use --dry-run first. Photos already in local time are left alone, so running
localize again only touches new photos. "reset" undoes it together with any
shift.`,
		Example: `  camsync localize --dry-run ~/Photos/trip
  camsync localize --clock-zone sony=Z ~/Photos/trip
  camsync localize --camera canon --zone +02:00 -v ~/Photos/trip`,
		Args:              dirArgs,
		ValidArgsFunction: completeDirs,
		RunE: rt.runE(func(cmd *cobra.Command, args []string) error {
			settings, err := clock.settings()
			if err != nil {
				return err
			}
			c := app.LocalizeCommand{Scope: rt.scope(args), ClockSettings: settings, Camera: camera, Zone: zone, DryRun: dryRun}
			out := cmd.OutOrStdout()
			h := app.LocalizeHandler{}
			if verbose && !rt.json() {
				h.Progress = func(_ string, e app.Entry) { printEntry(out, e) }
			}
			res, err := h.Handle(cmd.Context(), c)
			if err != nil {
				return err
			}
			if rt.json() {
				err = writeJSON(out, localizeView(res))
			} else {
				err = printLocalize(out, res, verbose)
			}
			if err != nil {
				return err
			}
			if res.Failed > 0 {
				return fmt.Errorf("%d photos could not be processed", res.Failed)
			}
			return nil
		}),
	}
	clock.register(cmd, true)
	f := cmd.Flags()
	f.StringVarP(&camera, "camera", "c", "", `only localize this camera, as printed by "list" (all cameras still provide evidence)`)
	f.StringVar(&zone, "zone", "", "local zone where no phone photo tells it, e.g. +02:00")
	f.BoolVarP(&dryRun, "dry-run", "n", false, "report what would change without writing anything")
	f.BoolVarP(&verbose, "verbose", "v", false, "also list every photo")
	registerCompletions(cmd, map[string]cobra.CompletionFunc{"camera": completeCamera, "zone": noFileComp})
	return cmd
}
