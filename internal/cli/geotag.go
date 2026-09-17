package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/app"
)

func (rt *runtime) geotagCommand() *cobra.Command {
	var clock clockFlags
	var camera, maxGap, maxBridge string
	var dryRun, verbose bool
	cmd := &cobra.Command{
		Use:   "geotag [DIR]",
		Short: "Estimate locations for photos without one from photos close in time",
		Long: `Geotag gives photos without a location the location of photos taken at
about the same time that have one, usually from a phone. It writes the
position to the sidecar marked as estimated (exif:GPSProcessingMethod
"camsync"), so "reset" can remove it again. Photos with a location of their
own, from the image or a sidecar written by another tool, are never touched,
and estimates are never used as evidence.

A photo between two located photos gets a position on the line between them,
by time, when both are within --max-gap (default 5m), or both within
--max-bridge (default 15m) and no more than 1 km apart: whoever photographed
the same square twice in a quarter of an hour was there in between.
Otherwise the nearest located photo within --max-gap is copied. Photos
further from any located photo are skipped.

The moment of each photo comes from its camera's clock and clock zone, as
for "localize", so run "suggest" and "shift" first. A located photo's moment
is its GPS time, which does not depend on any clock.

Use --dry-run first. The table shows how far in time the estimates reach;
-v lists every photo with the photos its position comes from.`,
		Example: `  camsync geotag --dry-run ~/Photos/trip
  camsync geotag --camera canon -v ~/Photos/trip
  camsync geotag --max-gap 2m --max-bridge 10m ~/Photos/trip`,
		Args:              dirArgs,
		ValidArgsFunction: completeDirs,
		RunE: rt.runE(func(cmd *cobra.Command, args []string) error {
			settings, err := clock.settings()
			if err != nil {
				return err
			}
			c := app.GeotagCommand{Scope: rt.scope(args), ClockSettings: settings, Camera: camera, DryRun: dryRun}
			for _, d := range []struct {
				flag, value string
				dst         *time.Duration
			}{{"--max-gap", maxGap, &c.Track.MaxGap}, {"--max-bridge", maxBridge, &c.Track.MaxBridge}} {
				if d.value == "" {
					continue
				}
				v, err := camsync.ParseShift(d.value)
				if err != nil {
					return usage(err)
				}
				if v <= 0 {
					return usage(errors.New(d.flag + " must be positive"))
				}
				*d.dst = v
			}
			out := cmd.OutOrStdout()
			h := app.GeotagHandler{}
			if verbose && !rt.json() {
				h.Progress = func(_ string, e app.Entry) { printEntry(out, e) }
			}
			res, err := h.Handle(cmd.Context(), c)
			if err != nil {
				return err
			}
			if rt.json() {
				err = writeJSON(out, geotagView(res))
			} else {
				err = printGeotag(out, res, verbose)
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
	clock.register(cmd, false)
	f := cmd.Flags()
	f.StringVarP(&camera, "camera", "c", "", `only geotag this camera, as printed by "list" (all cameras still provide evidence)`)
	f.StringVar(&maxGap, "max-gap", "", "how far in time a located photo reaches on its own (default 5m)")
	f.StringVar(&maxBridge, "max-bridge", "", "how far apart in time two nearby located photos may be to place a photo between them (default 15m)")
	f.BoolVarP(&dryRun, "dry-run", "n", false, "report what would change without writing anything")
	f.BoolVarP(&verbose, "verbose", "v", false, "also list every photo")
	registerCompletions(cmd, map[string]cobra.CompletionFunc{"camera": completeCamera, "max-gap": noFileComp, "max-bridge": noFileComp})
	return cmd
}

type geotaggedCameraView struct {
	Camera       string         `json:"camera"`
	ClockZone    *clockZoneView `json:"clockZone"`
	Located      int            `json:"located"`
	Interpolated int            `json:"interpolated"`
	MedianGap    string         `json:"medianGap"`
	MaxGap       string         `json:"maxGap"`
	Written      int            `json:"written"`
	Created      int            `json:"created"`
	Skipped      int            `json:"skipped"`
	Failed       int            `json:"failed"`
	Entries      []entryView    `json:"entries"`
}

type geotagJSON struct {
	DryRun    bool                  `json:"dryRun"`
	Evidence  int                   `json:"evidence"`
	Cameras   []geotaggedCameraView `json:"cameras"`
	Written   int                   `json:"written"`
	Created   int                   `json:"created"`
	Skipped   int                   `json:"skipped"`
	Unchanged int                   `json:"unchanged"`
	Located   int                   `json:"located"`
	Failed    int                   `json:"failed"`
}

func geotagView(res *app.GeotagResult) geotagJSON {
	v := geotagJSON{
		DryRun: res.DryRun, Evidence: res.Evidence, Cameras: []geotaggedCameraView{},
		Written: res.Written, Created: res.Created, Skipped: res.Skipped, Unchanged: res.Unchanged, Located: res.Located, Failed: res.Failed,
	}
	for _, c := range res.Cameras {
		cv := geotaggedCameraView{
			Camera: c.Camera, ClockZone: clockZoneJSON(c.ClockZone), Located: c.Located, Interpolated: c.Interpolated,
			MedianGap: gapText(c.MedianGap), MaxGap: gapText(c.MaxGap),
			Written: c.Written, Created: c.Created, Skipped: c.Skipped, Failed: c.Failed, Entries: []entryView{},
		}
		for _, e := range c.Entries {
			cv.Entries = append(cv.Entries, entryJSON(e))
		}
		v.Cameras = append(v.Cameras, cv)
	}
	return v
}

// printGeotag renders one row per camera, the reasons photos were skipped
// unless verbose already listed them, and a summary.
func printGeotag(w io.Writer, res *app.GeotagResult, verbose bool) error {
	if verbose {
		fmt.Fprintln(w)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CAMERA\tFILES\tLOCATED\tWRITTEN\tINTERPOLATED\tSKIPPED\tMEDIAN GAP\tMAX GAP")
	for _, c := range res.Cameras {
		median, maxGap := "-", "-"
		if c.Written > 0 {
			median, maxGap = gapText(c.MedianGap), gapText(c.MaxGap)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n", c.Camera, len(c.Entries), c.Located, c.Written, c.Interpolated, c.Skipped, median, maxGap)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !verbose {
		for _, c := range res.Cameras {
			printSkipReasons(w, c.ChangeResult)
		}
	}
	prefix := ""
	if res.DryRun {
		prefix = "dry run: "
	}
	_, err := fmt.Fprintf(w, "\n%s%d sidecars written (%d new), %d skipped (%d located, %d already geotagged), %d failed; positions from %d located photos\n",
		prefix, res.Written, res.Created, res.Skipped, res.Located, res.Unchanged, res.Failed, res.Evidence)
	return err
}

// gapText renders a duration without sign, "0s" for zero.
func gapText(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	return strings.TrimPrefix(camsync.FormatDuration(d.Abs()), "+")
}
