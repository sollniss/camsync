package cli

import (
	"errors"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/app"
)

// clockFlags are the flags of the clock estimation, shared by suggest,
// localize and geotag.
type clockFlags struct {
	references, clockZones []string
	maxOffset, zoneGap     string
}

func (c *clockFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVar(&c.references, "reference", nil, `trusted camera as printed by "list"; repeat for several (default: phones and cameras that agree with GPS)`)
	f.StringArrayVar(&c.clockZones, "clock-zone", nil, `zone a camera's clock runs on, as CAMERA=ZONE, e.g. "sony=Z" or "canon=+09:00"; repeat for several`)
	f.StringVar(&c.maxOffset, "max-offset", "", "largest clock error to search for, e.g. 30m or 14h (default 12h)")
	f.StringVar(&c.zoneGap, "zone-gap", "", "how far in time a phone photo tells the local zone (default 24h)")
	registerCompletions(cmd, map[string]cobra.CompletionFunc{"reference": completeCamera, "clock-zone": completeClockZone, "max-offset": noFileComp, "zone-gap": noFileComp})
}

func (c *clockFlags) settings() (app.ClockSettings, error) {
	s := app.ClockSettings{References: c.references, ClockZones: c.clockZones}
	err := parseDurations(durationFlag{"--max-offset", c.maxOffset, &s.MaxOffset}, durationFlag{"--zone-gap", c.zoneGap, &s.MaxZoneGap})
	return s, err
}

// durationFlag is a duration flag that must be positive when given.
type durationFlag struct {
	name, value string
	dst         *time.Duration
}

// parseDurations stores the values of the flags that were given. A bad value
// is a usage error naming the flag.
func parseDurations(flags ...durationFlag) error {
	for _, d := range flags {
		if d.value == "" {
			continue
		}
		v, err := camsync.ParseShift(d.value)
		if err != nil {
			return usage(err)
		}
		if v <= 0 {
			return usage(errors.New(d.name + " must be positive"))
		}
		*d.dst = v
	}
	return nil
}

// completeClockZone offers the camera names followed by "=".
func completeClockZone(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	names, directive := completeCamera(cmd, args, toComplete)
	var out []cobra.Completion
	for _, n := range names {
		name, _, _ := strings.Cut(n, "\t")
		out = append(out, name+"=")
	}
	return out, directive | cobra.ShellCompDirectiveNoSpace
}

func (rt *runtime) suggestCommand() *cobra.Command {
	var clock clockFlags
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

Photos are compared as moments in time, so every camera's CLOCK ZONE, the
zone its clock runs on, matters. It is the zone its photos record, "local"
for phones, the zone implied by GPS times, or, for a camera without any of
these, the whole hour that best lines up its wall clock with the references.
A camera set to UTC shows up as +00:00, one left on Japan time as +09:00.
Override a guess with --clock-zone CAMERA=ZONE.

CONFIDENCE is high when no other shift comes close to explaining the photos
equally well, and low when the photos are too few or too evenly spread to
tell. --max-offset limits the search (default 12h); beyond half a day the
previous or next day's activity easily looks like a match.`,
		Example: `  camsync suggest ~/Photos/trip
  camsync suggest --reference "Canon EOS R5" ~/Photos/trip
  camsync suggest --clock-zone sony=Z --max-offset 2h -o json ~/Photos/trip`,
		Args:              dirArgs,
		ValidArgsFunction: completeDirs,
		RunE: rt.runE(func(cmd *cobra.Command, args []string) error {
			settings, err := clock.settings()
			if err != nil {
				return err
			}
			q := app.SuggestQuery{Scope: rt.scope(args), ClockSettings: settings}
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
	clock.register(cmd)
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
