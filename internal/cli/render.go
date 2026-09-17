package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/app"
)

type photoView struct {
	Path         string         `json:"path"`
	Format       string         `json:"format"`
	Make         string         `json:"make,omitempty"`
	Model        string         `json:"model,omitempty"`
	FileOriginal *camsync.Stamp `json:"fileOriginal"`
	Original     *camsync.Stamp `json:"original"`
	Offset       *string        `json:"offset"`
	Sidecar      string         `json:"sidecar,omitempty"`
	Error        string         `json:"error,omitempty"`
}

type cameraView struct {
	Index       int            `json:"index"`
	Camera      string         `json:"camera"`
	Files       int            `json:"files"`
	First       *camsync.Stamp `json:"first"`
	Last        *camsync.Stamp `json:"last"`
	Offset      string         `json:"offset"`
	Offsets     map[string]int `json:"offsets"`
	Sidecars    int            `json:"sidecars"`
	WithoutDate int            `json:"withoutDate"`
	Errors      int            `json:"errors"`
	Photos      []photoView    `json:"photos"`
}

type listJSON struct {
	Cameras []cameraView `json:"cameras"`
	Photos  int          `json:"photos"`
	Ignored int          `json:"ignored"`
}

func listView(res *app.ListResult) listJSON {
	v := listJSON{Cameras: []cameraView{}, Photos: res.Photos, Ignored: res.Ignored}
	for _, c := range res.Cameras {
		cv := cameraView{
			Index: c.Index, Camera: c.Name, Files: len(c.Photos), First: c.First, Last: c.Last,
			Offset: c.OffsetText(), Offsets: map[string]int{}, Sidecars: c.Sidecars, WithoutDate: c.NoDate, Errors: c.Errors,
			Photos: []photoView{},
		}
		for d, n := range c.Offsets {
			cv.Offsets[camsync.FormatDuration(d)] = n
		}
		for _, p := range c.Photos {
			pv := photoView{Path: p.Path, Format: p.Format, Make: p.Make, Model: p.Model, FileOriginal: p.FileOriginal, Original: p.Original, Sidecar: p.Sidecar}
			if d, ok := p.Offset(); ok {
				s := camsync.FormatDuration(d)
				pv.Offset = &s
			}
			if p.Err != nil {
				pv.Error = p.Err.Error()
			}
			cv.Photos = append(cv.Photos, pv)
		}
		v.Cameras = append(v.Cameras, cv)
	}
	return v
}

type entryView struct {
	Path    string         `json:"path"`
	Action  app.Action     `json:"action"`
	Before  *camsync.Stamp `json:"before"`
	After   *camsync.Stamp `json:"after"`
	Fix     *fixView       `json:"position,omitempty"`
	Sidecar string         `json:"sidecar"`
	Reason  string         `json:"reason,omitempty"`
}

type fixView struct {
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	Gap          string  `json:"gap"`
	Interpolated bool    `json:"interpolated"`
	From         string  `json:"from"`
	To           string  `json:"to,omitempty"`
}

func entryJSON(e app.Entry) entryView {
	v := entryView{Path: e.Path, Action: e.Action, Before: e.Before, After: e.After, Sidecar: e.Sidecar, Reason: e.Reason}
	if f := e.Fix; f != nil {
		v.Fix = &fixView{Lat: f.Lat, Lon: f.Lon, Gap: gapText(f.Gap), Interpolated: f.Interpolated}
		if f.From != nil {
			v.Fix.From = f.From.Path
		}
		if f.To != nil {
			v.Fix.To = f.To.Path
		}
	}
	return v
}

type changeJSON struct {
	Camera  string      `json:"camera"`
	By      string      `json:"by"`
	Zone    string      `json:"zone,omitempty"`
	DryRun  bool        `json:"dryRun"`
	Entries []entryView `json:"entries"`
	Written int         `json:"written"`
	Created int         `json:"created"`
	Skipped int         `json:"skipped"`
	Failed  int         `json:"failed"`
}

func changeView(res *app.ChangeResult) changeJSON {
	v := changeJSON{
		Camera: res.Camera, By: camsync.FormatDuration(res.By), Zone: res.Zone, DryRun: res.DryRun,
		Entries: []entryView{}, Written: res.Written, Created: res.Created, Skipped: res.Skipped, Failed: res.Failed,
	}
	for _, e := range res.Entries {
		v.Entries = append(v.Entries, entryJSON(e))
	}
	return v
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func stampText(s *camsync.Stamp) string {
	if s == nil {
		return "-"
	}
	return s.String()
}

func printList(w io.Writer, res *app.ListResult, verbose bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tCAMERA\tFILES\tFIRST\tLAST\tOFFSET\tSIDECARS\tNOTES")
	for _, c := range res.Cameras {
		fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\t%d\t%s\n",
			c.Index, c.Name, len(c.Photos), stampText(c.First), stampText(c.Last), c.OffsetText(), c.Sidecars, c.Notes())
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if verbose {
		for _, c := range res.Cameras {
			fmt.Fprintf(w, "\n[%d] %s\n", c.Index, c.Name)
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "  FILE\tFORMAT\tIN FILE\tEFFECTIVE\tOFFSET\tSIDECAR\tNOTE")
			for _, p := range c.Photos {
				off := "-"
				if d, ok := p.Offset(); ok {
					off = camsync.FormatDuration(d)
				}
				sc := "-"
				if p.Sidecar != "" {
					sc = filepath.Base(p.Sidecar)
				}
				note := ""
				if p.Err != nil {
					note = p.Err.Error()
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					p.Path, p.Format, stampText(p.FileOriginal), stampText(p.Original), off, sc, note)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(w, "\n%d photos, %d cameras, %d other files ignored\n", res.Photos, len(res.Cameras), res.Ignored)
	return err
}

func printEntry(w io.Writer, e app.Entry) {
	switch e.Action {
	case app.ActionSkip:
		fmt.Fprintf(w, "skip    %s: %s\n", e.Path, e.Reason)
	case app.ActionFail:
		fmt.Fprintf(w, "FAILED  %s: %s\n", e.Path, e.Reason)
	default:
		if e.Fix != nil {
			fmt.Fprintf(w, "%-7s %s: %s  (%s)\n", e.Action, e.Path, e.Fix, filepath.Base(e.Sidecar))
			return
		}
		fmt.Fprintf(w, "%-7s %s: %s -> %s  (%s)\n", e.Action, e.Path, stampText(e.Before), stampText(e.After), filepath.Base(e.Sidecar))
	}
}

func printChangeSummary(w io.Writer, res *app.ChangeResult) {
	what := "reset to the image timestamps"
	if res.By != 0 || res.Zone != "" {
		what = "shifted by " + camsync.FormatDuration(res.By)
		if res.Zone != "" {
			what += ", zone " + res.Zone
		}
	}
	prefix := ""
	if res.DryRun {
		prefix = "dry run: "
	}
	fmt.Fprintf(w, "%s%q %s: %d sidecars written (%d new), %d skipped, %d failed\n",
		prefix, res.Camera, what, res.Written, res.Created, res.Skipped, res.Failed)
}

type gpsView struct {
	Photos       int    `json:"photos"`
	Error        string `json:"error"`
	Zone         string `json:"zone"`
	ZoneInferred bool   `json:"zoneInferred"`
}

type clockZoneView struct {
	Zone   string `json:"zone,omitempty"`
	Source string `json:"source"`
}

type suggestionView struct {
	Index      int            `json:"index"`
	Camera     string         `json:"camera"`
	Files      int            `json:"files"`
	Reference  bool           `json:"reference"`
	Basis      string         `json:"basis,omitempty"`
	ClockZone  *clockZoneView `json:"clockZone"`
	Shift      *string        `json:"shift"`
	Confidence string         `json:"confidence,omitempty"`
	Matched    int            `json:"matched"`
	GPS        *gpsView       `json:"gps"`
	Note       string         `json:"note,omitempty"`
}

type suggestJSON struct {
	MaxOffset  string           `json:"maxOffset"`
	WallClocks bool             `json:"wallClocks"`
	Cameras    []suggestionView `json:"cameras"`
}

func clockZoneJSON(z camsync.ClockZone) *clockZoneView {
	switch z.Source {
	case "":
		return nil
	case camsync.ZoneLocal:
		return &clockZoneView{Source: string(z.Source)}
	}
	return &clockZoneView{Zone: camsync.FormatZone(z.Zone), Source: string(z.Source)}
}

// clockZoneText renders a clock zone for tables, marking guesses.
func clockZoneText(z camsync.ClockZone) string {
	switch z.Source {
	case "":
		return "-"
	case camsync.ZoneFromActivity:
		return z.String() + " (guessed)"
	case camsync.ZoneFromGPS:
		return z.String() + " (GPS)"
	}
	return z.String()
}

func suggestView(res *app.SuggestResult) suggestJSON {
	v := suggestJSON{MaxOffset: camsync.FormatDuration(res.MaxOffset), WallClocks: res.WallClocks, Cameras: []suggestionView{}}
	for _, c := range res.Cameras {
		sv := suggestionView{
			Index: c.Index, Camera: c.Name, Files: c.Files, Reference: c.Reference, Basis: string(c.Basis), ClockZone: clockZoneJSON(c.ClockZone),
			Confidence: string(c.Confidence), Matched: c.Matched, Note: c.Note,
		}
		if c.Found {
			s := camsync.FormatDuration(c.Offset)
			sv.Shift = &s
		}
		if g := c.GPS; g != nil {
			sv.GPS = &gpsView{Photos: g.Photos, Error: camsync.FormatDuration(g.Error), Zone: camsync.FormatZone(g.Zone), ZoneInferred: g.ZoneInferred}
		}
		v.Cameras = append(v.Cameras, sv)
	}
	return v
}

// printSuggest renders a suggestion table followed by the shift commands
// that apply the estimates. shiftArgs are appended to every command.
func printSuggest(w io.Writer, res *app.SuggestResult, shiftArgs []string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tCAMERA\tFILES\tCLOCK\tCLOCK ZONE\tSHIFT\tCONFIDENCE\tNOTES")
	references := 0
	for _, c := range res.Cameras {
		clock, shift, conf := "unknown", "-", "-"
		switch {
		case c.Reference:
			clock = "reference"
			references++
		case c.Found:
			clock, shift, conf = "estimated", camsync.FormatDuration(c.Offset), string(c.Confidence)
		}
		fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", c.Index, c.Name, c.Files, clock, clockZoneText(c.ClockZone), shift, conf, suggestNotes(c))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if res.WallClocks {
		fmt.Fprintln(w, "\nNo photo records a time zone; wall clocks were compared.")
	}

	if references == 0 {
		_, err := fmt.Fprintln(w, "\nNo trusted clock found. Name a camera whose clock is right with --reference.")
		return err
	}
	var cmds []string
	for _, c := range res.Cameras {
		if !c.Found || c.Offset == 0 {
			continue
		}
		args := append([]string{"camsync", "shift", "--camera", c.Name, "--by", camsync.FormatDuration(c.Offset)}, shiftArgs...)
		for i, a := range args {
			args[i] = shellQuote(a)
		}
		line := "  " + strings.Join(args, " ")
		if c.Confidence == camsync.ConfidenceLow {
			line += "  # low confidence: compare a few photos first"
		}
		cmds = append(cmds, line)
	}
	if len(cmds) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "\nTo apply:\n%s\n", strings.Join(cmds, "\n"))
	return err
}

// suggestNotes explains one row of the suggestion table.
func suggestNotes(c app.Suggestion) string {
	var parts []string
	switch {
	case c.Reference:
		switch c.Basis {
		case camsync.BasisPhone:
			parts = append(parts, "phone")
		case camsync.BasisUser:
			parts = append(parts, "named with --reference")
		}
	case c.Basis == camsync.BasisGPS:
		parts = append(parts, fmt.Sprintf("measured against GPS time in %d photos", c.Matched))
	case c.Basis == camsync.BasisActivity:
		parts = append(parts, fmt.Sprintf("%d photos within %s of a reference photo", c.Matched, strings.TrimPrefix(camsync.FormatDuration(camsync.MatchWindow), "+")))
	case c.Note != "":
		parts = append(parts, c.Note)
	}
	if g := c.GPS; g != nil && (c.Reference || c.Basis != camsync.BasisGPS) {
		gps := "GPS: clock " + camsync.FormatDuration(g.Error)
		if g.Error == 0 {
			gps = "GPS: clock exact"
		}
		if g.ZoneInferred {
			gps += " (zone unknown, assumed " + camsync.FormatZone(g.Zone) + ")"
		}
		parts = append(parts, gps)
	}
	return strings.Join(parts, "; ")
}

var shellSafeRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./~-]+$`)

// shellQuote quotes s for POSIX shells when it contains anything special.
func shellQuote(s string) string {
	if shellSafeRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type localizedCameraView struct {
	Camera     string         `json:"camera"`
	ClockZone  *clockZoneView `json:"clockZone"`
	LocalZones []string       `json:"localZones"`
	DayChanges int            `json:"dayChanges"`
	Written    int            `json:"written"`
	Created    int            `json:"created"`
	Skipped    int            `json:"skipped"`
	Failed     int            `json:"failed"`
	Entries    []entryView    `json:"entries"`
}

type localizeJSON struct {
	DryRun    bool                  `json:"dryRun"`
	Evidence  int                   `json:"evidence"`
	Cameras   []localizedCameraView `json:"cameras"`
	Written   int                   `json:"written"`
	Created   int                   `json:"created"`
	Skipped   int                   `json:"skipped"`
	Unchanged int                   `json:"unchanged"`
	Failed    int                   `json:"failed"`
}

func localizeView(res *app.LocalizeResult) localizeJSON {
	v := localizeJSON{
		DryRun: res.DryRun, Evidence: res.Evidence, Cameras: []localizedCameraView{},
		Written: res.Written, Created: res.Created, Skipped: res.Skipped, Unchanged: res.Unchanged, Failed: res.Failed,
	}
	for _, c := range res.Cameras {
		cv := localizedCameraView{
			Camera: c.Camera, ClockZone: clockZoneJSON(c.ClockZone), LocalZones: append([]string{}, c.LocalZones...), DayChanges: c.DayChanges,
			Written: c.Written, Created: c.Created, Skipped: c.Skipped, Failed: c.Failed, Entries: []entryView{},
		}
		for _, e := range c.Entries {
			cv.Entries = append(cv.Entries, entryJSON(e))
		}
		v.Cameras = append(v.Cameras, cv)
	}
	return v
}

// printSkipReasons prints how many photos of a camera were skipped or failed
// for each reason, in order of first appearance.
func printSkipReasons(w io.Writer, res app.ChangeResult) {
	reasons := map[string]int{}
	var order []string
	for _, e := range res.Entries {
		if e.Action != app.ActionSkip && e.Action != app.ActionFail {
			continue
		}
		if reasons[e.Reason] == 0 {
			order = append(order, e.Reason)
		}
		reasons[e.Reason]++
	}
	for _, r := range order {
		fmt.Fprintf(w, "%s: %d %s\n", res.Camera, reasons[r], r)
	}
}

// printLocalize renders one row per camera, the reasons photos were skipped
// unless verbose already listed them, and a summary.
func printLocalize(w io.Writer, res *app.LocalizeResult, verbose bool) error {
	if verbose {
		fmt.Fprintln(w)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CAMERA\tFILES\tCLOCK ZONE\tLOCAL ZONE\tWRITTEN\tSKIPPED\tDAY CHANGES")
	for _, c := range res.Cameras {
		zones := strings.Join(c.LocalZones, ", ")
		if zones == "" {
			zones = "-"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%d\t%d\n", c.Camera, len(c.Entries), clockZoneText(c.ClockZone), zones, c.Written, c.Skipped, c.DayChanges)
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
	_, err := fmt.Fprintf(w, "\n%s%d sidecars written (%d new), %d skipped (%d already local), %d failed; local zone from %d phone photos\n",
		prefix, res.Written, res.Created, res.Skipped, res.Unchanged, res.Failed, res.Evidence)
	return err
}
