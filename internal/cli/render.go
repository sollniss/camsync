package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
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
	Sidecar string         `json:"sidecar"`
	Reason  string         `json:"reason,omitempty"`
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
		v.Entries = append(v.Entries, entryView{Path: e.Path, Action: e.Action, Before: e.Before, After: e.After, Sidecar: e.Sidecar, Reason: e.Reason})
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

type suggestionView struct {
	Index      int      `json:"index"`
	Camera     string   `json:"camera"`
	Files      int      `json:"files"`
	Reference  bool     `json:"reference"`
	Basis      string   `json:"basis,omitempty"`
	Shift      *string  `json:"shift"`
	Confidence string   `json:"confidence,omitempty"`
	Matched    int      `json:"matched"`
	GPS        *gpsView `json:"gps"`
	Note       string   `json:"note,omitempty"`
}

type suggestJSON struct {
	MaxOffset string           `json:"maxOffset"`
	Cameras   []suggestionView `json:"cameras"`
}

func suggestView(res *app.SuggestResult) suggestJSON {
	v := suggestJSON{MaxOffset: camsync.FormatDuration(res.MaxOffset), Cameras: []suggestionView{}}
	for _, c := range res.Cameras {
		sv := suggestionView{
			Index: c.Index, Camera: c.Name, Files: c.Files, Reference: c.Reference, Basis: string(c.Basis),
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
	fmt.Fprintln(tw, "#\tCAMERA\tFILES\tCLOCK\tSHIFT\tCONFIDENCE\tNOTES")
	references := 0
	var refZones []string
	for _, c := range res.Cameras {
		if c.Reference {
			references++
			if c.Zone != "" && !slices.Contains(refZones, c.Zone) {
				refZones = append(refZones, c.Zone)
			}
		}
	}
	for _, c := range res.Cameras {
		clock, shift, conf := "unknown", "-", "-"
		switch {
		case c.Reference:
			clock = "reference"
		case c.Found:
			clock, shift, conf = "estimated", camsync.FormatDuration(c.Offset), string(c.Confidence)
		}
		notes := suggestNotes(c)
		if !c.Reference && c.Zone != "" && len(refZones) > 0 && !slices.Contains(refZones, c.Zone) {
			// Worth a look: either the camera was left on another zone, which
			// is fine, or its zone setting is wrong and needs --timezone.
			notes = strings.TrimPrefix(notes+"; zone "+c.Zone+", references "+strings.Join(refZones, ", "), "; ")
		}
		fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\t%s\n", c.Index, c.Name, c.Files, clock, shift, conf, notes)
	}
	if err := tw.Flush(); err != nil {
		return err
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
