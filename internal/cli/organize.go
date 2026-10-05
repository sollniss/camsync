package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/app"
	"github.com/sollniss/camsync/internal/web"
	"github.com/sollniss/camsync/preview"
)

func (rt *runtime) organizeCommand() *cobra.Command {
	var open, dryRun, verbose, undo bool
	var listen, tiles, planFile, applyFile, gap, jump, home, homeRadius, minTrip, layout string
	var minTripPhotos int
	var inboxes []string
	cmd := &cobra.Command{
		Use:   "organize [DIR]",
		Short: "Find the events in a photo library and suggest where photos belong",
		Long: `Organize scans a whole library, groups the photos of all cameras into
events by time and place, works out what each folder holds and suggests
moves that tidy the library up. The result is served to the browser with a
timeline, a map and previews, where suggestions can be accepted one by one
and applied. Nothing is moved unless a plan is applied.

An event is a run of photos with no pause longer than --gap between them
and no jump further than --jump between located photos. Photos are placed
by their wall clock, so cameras whose clocks are hours apart still share
an event, and an event that runs into the next day away from home is a
trip. Home is where photos were taken on the most days; give it with
--home when the guess is wrong.

Folders are read as one of:
  event    holds one event or a few close together, e.g. a trip
  period   a calendar year or month by its name: 2024, 2024-05, 2024/05
  inbox    where photos land before they are sorted: the root itself, a
           folder named inbox, unsorted, import, DCIM and the like, or
           any folder given with --inbox
  mixed    many events, no recognisable scheme

Suggestions are:
  merge    an event folder holding the same event as another one, e.g. one
           folder per camera for the same trip
  move     photos into the folder that holds their event, or a stray back
           to the inbox, or a photo out of a period it was not taken in
  create   a trip found in the inbox that has no folder yet
  stray    photos that do not belong where they are and have nowhere to go

Confidence is high when the event is placed by positions on both sides,
medium when one side is placed by time alone and low when the event has no
positions at all. --layout trips also pulls trips out of period folders;
--layout periods never creates trip folders.

The server runs until it is interrupted; --open also opens the browser.
The map loads its tiles from OpenStreetMap, which learns which areas are
looked at; --tiles none turns it off.
Without a browser, --plan writes every suggestion with a destination to a
file to review by hand and -o json prints the whole picture; neither
starts the server. --apply carries out a plan file, or one saved from the
browser.
Every applied plan is recorded under DIR/.camsync/journal, and --undo
moves its photos back. Moving files confuses catalog applications such as
Lightroom, which reference files by path.`,
		Example: `  camsync organize ~/Photos
  camsync organize --open ~/Photos
  camsync organize --inbox Inbox --home 52.52,13.40 ~/Photos
  camsync organize --plan plan.json ~/Photos && camsync organize --apply plan.json --dry-run
  camsync organize --undo ~/Photos
  camsync organize --gap 12h --jump 20km --layout trips -o json ~/Photos`,
		Args:              dirArgs,
		ValidArgsFunction: completeDirs,
		RunE: rt.runE(func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if undo && applyFile != "" {
				return usage(errors.New("--undo and --apply exclude each other"))
			}
			if undo {
				return rt.undo(cmd.Context(), out, rt.scope(args).Dir, dryRun, verbose)
			}
			if applyFile != "" {
				return rt.applyPlan(cmd.Context(), out, applyFile, dryRun, verbose)
			}
			opts := camsync.LibraryOptions{Inboxes: inboxes}
			if err := parseDurations(durationFlag{"--gap", gap, &opts.MaxGap}, durationFlag{"--min-trip", minTrip, &opts.MinTripSpan}); err != nil {
				return err
			}
			if err := parseDistances(distanceFlag{"--jump", jump, &opts.MaxJump}, distanceFlag{"--home-radius", homeRadius, &opts.HomeRadius}); err != nil {
				return err
			}
			if minTripPhotos < 0 {
				return usage(errors.New("--min-trip-photos must not be negative"))
			}
			opts.MinTripPhotos = minTripPhotos
			if home != "" {
				p, err := camsync.ParsePosition(home)
				if err != nil {
					return usage(err)
				}
				opts.Home = &p
			}
			l, err := camsync.ParseLayout(layout)
			if err != nil {
				return usage(err)
			}
			opts.Layout = l
			q := app.OrganizeQuery{Scope: rt.scope(args), Options: opts}
			res, err := app.OrganizeHandler{}.Handle(cmd.Context(), q)
			if err != nil {
				return err
			}
			moves := 0
			if planFile != "" {
				if moves, err = writePlan(planFile, res); err != nil {
					return err
				}
			}
			if rt.json() {
				return writeJSON(out, web.View(res))
			}
			if planFile != "" {
				fmt.Fprintf(out, "%d photos to move. Plan written to %s; review it, then run: camsync organize --apply %s --dry-run\n", moves, planFile, planFile)
				return nil
			}
			return serveUI(cmd.Context(), cmd.ErrOrStderr(), q.Scope, res, listen, open, web.Config{Tiles: tileURL(tiles)})
		}),
	}
	f := cmd.Flags()
	f.StringVar(&listen, "listen", "127.0.0.1:0", "address the server listens on")
	f.StringVar(&tiles, "tiles", web.OSMTiles, `map tiles, as a URL with {z}, {x} and {y}, or "none" for no map`)
	f.BoolVar(&open, "open", false, "open the browser once the server listens")
	f.StringVar(&planFile, "plan", "", "write the suggestions as a plan file to apply later instead of serving them")
	f.StringVar(&applyFile, "apply", "", "carry out the moves of a plan file instead of scanning")
	f.BoolVar(&undo, "undo", false, "move the photos of the plan last applied to DIR back; repeat to go further back")
	f.BoolVarP(&dryRun, "dry-run", "n", false, "with --apply or --undo, report what would move without moving anything")
	f.StringVar(&gap, "gap", "", "pause in shooting that separates two events (default 1d)")
	f.StringVar(&jump, "jump", "", "distance between consecutive located photos that separates two events, e.g. 20km (default 50km)")
	f.StringVar(&home, "home", "", "where the photos' owner lives, as LAT,LON (default: inferred)")
	f.StringVar(&homeRadius, "home-radius", "", "how far from home a photo still counts as at home (default 30km)")
	f.StringVar(&minTrip, "min-trip", "", "how long an event away from home must last to be a trip (default 18h)")
	f.IntVar(&minTripPhotos, "min-trip-photos", 0, "how many photos an event without a location needs to be a trip (default 10)")
	f.StringVar(&layout, "layout", "auto", `"auto", "trips" (pull trips out of period folders too) or "periods" (never create trip folders)`)
	f.StringArrayVar(&inboxes, "inbox", nil, "folder where photos land before they are sorted, relative to DIR; repeat for several")
	f.BoolVarP(&verbose, "verbose", "v", false, "with --apply or --undo, list every move")
	registerCompletions(cmd, map[string]cobra.CompletionFunc{
		"layout": cobra.FixedCompletions([]cobra.Completion{
			cobra.CompletionWithDesc("auto", "keep every folder's own scheme"),
			cobra.CompletionWithDesc("trips", "give every trip its own folder"),
			cobra.CompletionWithDesc("periods", "never create trip folders"),
		}, cobra.ShellCompDirectiveNoFileComp),
		"inbox": func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveFilterDirs
		},
		"gap": noFileComp, "jump": noFileComp, "home": noFileComp, "home-radius": noFileComp, "min-trip": noFileComp, "min-trip-photos": noFileComp, "listen": noFileComp, "tiles": noFileComp,
	})
	for _, name := range []string{"plan", "apply"} {
		if err := cmd.MarkFlagFilename(name, "json"); err != nil {
			panic(err)
		}
	}
	return cmd
}

// distanceFlag is a distance flag that must be positive when given.
type distanceFlag struct {
	name, value string
	dst         *float64
}

func parseDistances(flags ...distanceFlag) error {
	for _, d := range flags {
		if d.value == "" {
			continue
		}
		v, err := camsync.ParseDistance(d.value)
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

// tileURL reads the --tiles flag: "none" or empty for no map.
func tileURL(flag string) string {
	if strings.EqualFold(flag, "none") {
		return ""
	}
	return flag
}

// loopback reports whether a listen address only accepts connections from
// this machine.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serveUI runs the browser interface until the context is cancelled.
func serveUI(ctx context.Context, msgs io.Writer, scope app.Scope, res *app.OrganizeResult, listen string, open bool, cfg web.Config) error {
	var cache *preview.Cache
	if dir, err := preview.DefaultCacheDir(); err == nil {
		cache = preview.NewCache(dir, 0)
		go func() { _, _ = cache.Prune(preview.DefaultCacheLimit) }()
	}
	if !loopback(listen) {
		fmt.Fprintf(msgs, "warning: listening on %s without authentication; anyone who can reach it can see and move the photos\n", listen)
	}
	srv := web.New(scope, res, cache, cfg)
	err := web.Serve(ctx, listen, srv, func(url string) {
		fmt.Fprintf(msgs, "Serving %s at %s (Ctrl-C to stop)\n", scope.Dir, url)
		if open {
			if err := openBrowser(url); err != nil {
				fmt.Fprintf(msgs, "could not open the browser: %v\n", err)
			}
		}
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// openBrowser asks the desktop to open url.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// writePlan writes every suggestion that has a destination and reports how
// many photos the plan moves.
func writePlan(path string, res *app.OrganizeResult) (int, error) {
	plan := camsync.Plan{Root: res.Root, Moves: []camsync.Move{}}
	for _, s := range res.Suggestions {
		plan.Moves = append(plan.Moves, s.Moves()...)
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return 0, err
	}
	return len(plan.Moves), os.WriteFile(path, append(data, '\n'), 0o644)
}

// applyPlan reads a plan file and carries it out.
func (rt *runtime) applyPlan(ctx context.Context, out io.Writer, path string, dryRun, verbose bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var plan camsync.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return usage(fmt.Errorf("%s: %w", path, err))
	}
	h := app.ApplyHandler{}
	if verbose && !rt.json() {
		h.Progress = func(r camsync.MoveResult) { printMove(out, r) }
	}
	res, err := h.Handle(ctx, app.ApplyCommand{Plan: plan, DryRun: dryRun})
	if err != nil {
		return err
	}
	if err := rt.printApply(out, res.ApplyResult, verbose, "moved"); err != nil {
		return err
	}
	if res.JournalErr != nil {
		return fmt.Errorf("the moves could not be recorded for --undo: %w", res.JournalErr)
	}
	if res.Journal != "" && !rt.json() {
		fmt.Fprintf(out, "Recorded in %s; undo with: camsync organize --undo %s\n", res.Journal, plan.Root)
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d photos could not be moved", res.Failed)
	}
	return nil
}

// undo moves the photos of the plan last applied to root back.
func (rt *runtime) undo(ctx context.Context, out io.Writer, root string, dryRun, verbose bool) error {
	h := app.UndoHandler{}
	if verbose && !rt.json() {
		h.Progress = func(r camsync.MoveResult) { printMove(out, r) }
	}
	res, err := h.Handle(ctx, app.UndoCommand{Root: root, DryRun: dryRun})
	if res == nil {
		return err
	}
	if !rt.json() {
		fmt.Fprintf(out, "Undoing the plan applied %s\n", res.Applied.Local().Format("2006-01-02 15:04:05"))
	}
	if err := rt.printApply(out, res.ApplyResult, verbose, "moved back"); err != nil {
		return err
	}
	if err != nil {
		return err
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d photos could not be moved back; they stay in the journal to try again", res.Failed)
	}
	return nil
}

// printApply reports the outcome of moving photos: the failures, or every
// move with verbose, and a summary.
func (rt *runtime) printApply(out io.Writer, res *camsync.ApplyResult, verbose bool, moved string) error {
	if rt.json() {
		return writeJSON(out, applyView(res))
	}
	if !verbose {
		for _, r := range res.Results {
			if r.Err != nil {
				printMove(out, r)
			}
		}
	}
	prefix := ""
	if res.DryRun {
		prefix = "dry run: "
	}
	fmt.Fprintf(out, "%s%d photos %s, %d failed", prefix, res.Moved, moved, res.Failed)
	if len(res.Created) > 0 {
		fmt.Fprintf(out, ", %d folders created", len(res.Created))
	}
	fmt.Fprintln(out)
	return nil
}

func printMove(w io.Writer, r camsync.MoveResult) {
	if r.Err != nil {
		fmt.Fprintf(w, "FAILED  %s: %v\n", r.Path, r.Err)
		return
	}
	fmt.Fprintf(w, "move    %s -> %s\n", r.Path, r.To)
}

type applyJSON struct {
	DryRun  bool             `json:"dryRun"`
	Moved   int              `json:"moved"`
	Failed  int              `json:"failed"`
	Created []string         `json:"created"`
	Results []moveResultView `json:"results"`
}

type moveResultView struct {
	Path    string `json:"path"`
	Sidecar string `json:"sidecar,omitempty"`
	To      string `json:"to"`
	NewPath string `json:"newPath"`
	Error   string `json:"error,omitempty"`
}

func applyView(res *camsync.ApplyResult) applyJSON {
	v := applyJSON{DryRun: res.DryRun, Moved: res.Moved, Failed: res.Failed, Created: res.Created, Results: []moveResultView{}}
	if v.Created == nil {
		v.Created = []string{}
	}
	sort.Strings(v.Created)
	for _, r := range res.Results {
		mv := moveResultView{Path: r.Path, Sidecar: r.Sidecar, To: r.To, NewPath: r.NewPath}
		if r.Err != nil {
			mv.Error = r.Err.Error()
		}
		v.Results = append(v.Results, mv)
	}
	return v
}
