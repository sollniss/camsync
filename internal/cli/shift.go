package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/app"
)

func (rt *runtime) shiftCommand() *cobra.Command {
	var camera, by, tz string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "shift [DIR]",
		Short: "Move the timestamps of one camera's photos",
		Long: `Shift adds --by to the timestamps of every photo taken with --camera and
writes the result to XMP sidecars. Shifts accumulate: running --by 30s twice
shifts by one minute. Timestamps are read from the sidecar first and from the
image as fallback, so "list" always shows the effective values.

--timezone (alias --tz) stamps a UTC offset on the written dates. Use it for
cameras that record
no zone: Immich and other readers treat a zone-less sidecar date as UTC.
Zones found in the image (EXIF OffsetTime, or the Canon and Nikon maker
notes) or in an existing sidecar are kept automatically. --timezone may be used
without --by to fix the zone alone.`,
		Example: `  camsync shift --camera sony --by 2m30s ~/Photos/trip
  camsync shift --camera "Canon EOS R5" --by -45s --dry-run ~/Photos/trip
  camsync shift --camera 2 --timezone +02:00 ~/Photos/trip`,
		Args:              dirArgs,
		ValidArgsFunction: completeDirs,
		RunE: rt.runE(func(cmd *cobra.Command, args []string) error {
			c := app.ShiftCommand{Scope: rt.scope(args), Camera: camera, Zone: tz, DryRun: dryRun}
			if by != "" {
				d, err := camsync.ParseShift(by)
				if err != nil {
					return usage(err)
				}
				c.By = d
			}
			// A zero shift is fine when a zone is stamped; otherwise there
			// is nothing to do.
			switch {
			case by == "" && tz == "":
				return usage(errors.New("--by or --timezone is required"))
			case c.By == 0 && tz == "":
				return usage(errors.New("shift must not be zero (unless --timezone is given)"))
			}
			out := cmd.OutOrStdout()
			h := app.ShiftHandler{}
			if !rt.json() {
				h.Progress = func(e app.Entry) { printEntry(out, e) }
			}
			res, err := h.Handle(cmd.Context(), c)
			return rt.finish(out, res, err)
		}),
	}
	f := cmd.Flags()
	f.StringVarP(&camera, "camera", "c", "", `camera as printed by "list": exact name, row number or unique substring`)
	f.StringVar(&by, "by", "", "amount to shift by, e.g. 2m30s, -45s, 1h, +1d2h (a leading sign applies to the whole value)")
	f.StringVar(&tz, "timezone", "", "UTC offset to stamp on the written timestamps, e.g. +02:00 or Z (alias: --tz)")
	f.SetNormalizeFunc(aliasFlags(map[string]string{"tz": "timezone"}))
	registerCompletions(cmd, map[string]cobra.CompletionFunc{"camera": completeCamera, "by": noFileComp, "timezone": noFileComp})
	f.BoolVarP(&dryRun, "dry-run", "n", false, "report what would change without writing anything")
	mustMarkRequired(cmd, "camera")
	return cmd
}

func (rt *runtime) resetCommand() *cobra.Command {
	var camera string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "reset [DIR]",
		Short: "Restore the timestamps recorded in the image files",
		Long: `Reset writes the timestamps found in the image files back into the
sidecars of every photo taken with --camera, undoing all shifts. Sidecars are
kept, since they may hold other data.`,
		Example:           `  camsync reset --camera sony ~/Photos/trip`,
		Args:              dirArgs,
		ValidArgsFunction: completeDirs,
		RunE: rt.runE(func(cmd *cobra.Command, args []string) error {
			c := app.ResetCommand{Scope: rt.scope(args), Camera: camera, DryRun: dryRun}
			out := cmd.OutOrStdout()
			h := app.ResetHandler{}
			if !rt.json() {
				h.Progress = func(e app.Entry) { printEntry(out, e) }
			}
			res, err := h.Handle(cmd.Context(), c)
			return rt.finish(out, res, err)
		}),
	}
	f := cmd.Flags()
	f.StringVarP(&camera, "camera", "c", "", `camera as printed by "list": exact name, row number or unique substring`)
	f.BoolVarP(&dryRun, "dry-run", "n", false, "report what would change without writing anything")
	registerCompletions(cmd, map[string]cobra.CompletionFunc{"camera": completeCamera})
	mustMarkRequired(cmd, "camera")
	return cmd
}

// finish renders a change result and turns write failures into an error so
// the process exits non-zero.
func (rt *runtime) finish(out io.Writer, res *app.ChangeResult, err error) error {
	if err != nil {
		return err
	}
	if rt.json() {
		if err := writeJSON(out, changeView(res)); err != nil {
			return err
		}
	} else {
		printChangeSummary(out, res)
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d photos could not be processed", res.Failed)
	}
	return nil
}

func mustMarkRequired(cmd *cobra.Command, name string) {
	if err := cmd.MarkFlagRequired(name); err != nil {
		panic(err) // programmer error: the flag was not defined
	}
}

// aliasFlags returns a pflag normalize function that lets a flag be given
// under additional long names, e.g. --tz for --timezone. pflag shorthands are
// limited to one character, so this is the way to offer a longer short form.
func aliasFlags(aliases map[string]string) func(*pflag.FlagSet, string) pflag.NormalizedName {
	return func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if canonical, ok := aliases[name]; ok {
			return pflag.NormalizedName(canonical)
		}
		return pflag.NormalizedName(name)
	}
}
