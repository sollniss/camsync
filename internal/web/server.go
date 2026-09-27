// Package web serves the organize user interface: a local HTTP server with
// a JSON API over the application layer and an embedded single-page front
// end. It binds to the loopback interface and refuses to leave the library:
// previews are served for scanned photos only and moves are validated
// against the root before they are applied.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/app"
	"github.com/sollniss/camsync/preview"
)

//go:embed static
var static embed.FS

// MaxPreviewSize is the largest preview the API renders.
const MaxPreviewSize = 1600

// Server holds one scanned library and answers the front end.
type Server struct {
	mu     sync.RWMutex
	scope  app.Scope
	res    *app.OrganizeResult
	byPath map[string]*camsync.Photo
	cache  *preview.Cache
	mux    *http.ServeMux
}

// New wraps a scanned library. cache may be nil, in which case previews
// are rendered on every request.
func New(scope app.Scope, res *app.OrganizeResult, cache *preview.Cache) *Server {
	s := &Server{scope: scope, cache: cache, mux: http.NewServeMux()}
	s.set(res)
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	s.mux.Handle("GET /", http.FileServerFS(sub))
	s.mux.HandleFunc("GET /api/library", s.library)
	s.mux.HandleFunc("POST /api/rescan", s.rescan)
	s.mux.HandleFunc("GET /api/preview", s.preview)
	s.mux.HandleFunc("POST /api/apply", s.apply)
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) set(res *app.OrganizeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.res = res
	s.byPath = make(map[string]*camsync.Photo, len(res.Photos))
	for _, p := range res.Photos {
		s.byPath[p.Path] = p
	}
}

// Serve listens on addr, tells ready the URL, and serves until ctx is
// cancelled. addr may end in ":0" to pick a free port.
func Serve(ctx context.Context, addr string, s *Server, ready func(url string)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "localhost"
	}
	if ready != nil {
		ready("http://" + net.JoinHostPort(host, port) + "/")
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		<-done
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// sameOrigin rejects state-changing requests that a page on another origin
// could send. Browsers add an Origin header to cross-site POSTs and the
// custom header forces a preflight, which the server never answers.
func sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Camsync") == "" {
		http.Error(w, "missing X-Camsync header", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// library answers with the library planned with the options in the query,
// or the current ones when the query has none.
func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	res := s.res
	s.mu.RUnlock()
	opts, changed, err := parseOptions(r.URL.Query(), res.Options)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if changed {
		res = res.With(opts)
		s.set(res)
	}
	writeJSON(w, libraryView(res))
}

// rescan reads the library from disk again with the current options.
func (s *Server) rescan(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	s.mu.RLock()
	opts := s.res.Options
	s.mu.RUnlock()
	res, err := app.OrganizeHandler{}.Handle(r.Context(), app.OrganizeQuery{Scope: s.scope, Options: opts})
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	s.set(res)
	writeJSON(w, libraryView(res))
}

// preview serves a scaled JPEG of a scanned photo.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	s.mu.RLock()
	p := s.byPath[path]
	s.mu.RUnlock()
	if p == nil {
		http.Error(w, "not a photo of this library", http.StatusNotFound)
		return
	}
	size := preview.DefaultSize
	if v := r.URL.Query().Get("size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "bad size", http.StatusBadRequest)
			return
		}
		size = min(n, MaxPreviewSize)
	}
	var data []byte
	var err error
	if s.cache != nil {
		data, err = s.cache.Get(p.Path, size, p.Orientation)
	} else {
		data, err = preview.Extract(p.Path, size, p.Orientation)
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, preview.ErrNoPreview) {
			status = http.StatusUnsupportedMediaType
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

type applyRequest struct {
	Moves  []camsync.Move `json:"moves"`
	DryRun bool           `json:"dryRun"`
}

type applyResponse struct {
	Moved   int              `json:"moved"`
	Failed  int              `json:"failed"`
	DryRun  bool             `json:"dryRun"`
	Created []string         `json:"created"`
	Results []moveResultView `json:"results"`
	Library *LibraryJSON     `json:"library,omitempty"`
}

type moveResultView struct {
	Path    string `json:"path"`
	To      string `json:"to"`
	NewPath string `json:"newPath"`
	Error   string `json:"error,omitempty"`
}

// apply moves the given photos and, unless it was a dry run, rescans.
func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	var req applyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		writeError(w, fmt.Errorf("bad request: %w", err), http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	root, opts := s.res.Root, s.res.Options
	byPath := s.byPath
	s.mu.RUnlock()
	// Only photos of this library move, and their sidecars come from the
	// scan rather than from the request.
	moves := make([]camsync.Move, 0, len(req.Moves))
	for _, m := range req.Moves {
		p := byPath[m.Path]
		if p == nil {
			writeError(w, fmt.Errorf("%s is not a photo of this library", m.Path), http.StatusBadRequest)
			return
		}
		moves = append(moves, camsync.Move{Path: p.Path, Sidecar: p.Sidecar, To: m.To})
	}
	res, err := app.ApplyHandler{}.Handle(r.Context(), app.ApplyCommand{Plan: camsync.Plan{Root: root, Moves: moves}, DryRun: req.DryRun})
	if err != nil {
		status := http.StatusInternalServerError
		var verr *app.ValidationError
		if errors.As(err, &verr) {
			status = http.StatusBadRequest
		}
		writeError(w, err, status)
		return
	}
	out := applyResponse{Moved: res.Moved, Failed: res.Failed, DryRun: res.DryRun, Created: res.Created, Results: []moveResultView{}}
	if out.Created == nil {
		out.Created = []string{}
	}
	for _, mr := range res.Results {
		v := moveResultView{Path: mr.Path, To: mr.To, NewPath: mr.NewPath}
		if mr.Err != nil {
			v.Error = mr.Err.Error()
		}
		out.Results = append(out.Results, v)
	}
	if !req.DryRun && res.Moved > 0 {
		fresh, err := app.OrganizeHandler{}.Handle(r.Context(), app.OrganizeQuery{Scope: s.scope, Options: opts})
		if err == nil {
			s.set(fresh)
			lib := libraryView(fresh)
			out.Library = &lib
		}
	}
	writeJSON(w, out)
}

// parseOptions reads the planning options from a query, starting from the
// current ones. It reports whether anything changed.
func parseOptions(q url.Values, cur camsync.LibraryOptions) (camsync.LibraryOptions, bool, error) {
	opts := cur
	changed := false
	dur := func(key string, dst *time.Duration) error {
		v := q.Get(key)
		if v == "" {
			return nil
		}
		d, err := camsync.ParseShift(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s: %q is not a positive duration", key, v)
		}
		if *dst != d {
			*dst, changed = d, true
		}
		return nil
	}
	dist := func(key string, dst *float64) error {
		v := q.Get(key)
		if v == "" {
			return nil
		}
		d, err := camsync.ParseDistance(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s: %q is not a positive distance", key, v)
		}
		if *dst != d {
			*dst, changed = d, true
		}
		return nil
	}
	if err := errors.Join(
		dur("gap", &opts.MaxGap), dist("jump", &opts.MaxJump), dist("homeRadius", &opts.HomeRadius), dur("minTrip", &opts.MinTripSpan),
	); err != nil {
		return cur, false, err
	}
	if v := q.Get("minTripPhotos"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return cur, false, fmt.Errorf("minTripPhotos: %q is not a positive number", v)
		}
		if opts.MinTripPhotos != n {
			opts.MinTripPhotos, changed = n, true
		}
	}
	if q.Has("home") {
		v := strings.TrimSpace(q.Get("home"))
		if v == "" {
			if opts.Home != nil {
				opts.Home, changed = nil, true
			}
		} else {
			p, err := camsync.ParsePosition(v)
			if err != nil {
				return cur, false, err
			}
			if opts.Home == nil || *opts.Home != p {
				opts.Home, changed = &p, true
			}
		}
	}
	if v := q.Get("layout"); v != "" {
		l, err := camsync.ParseLayout(v)
		if err != nil {
			return cur, false, err
		}
		if opts.Layout != l {
			opts.Layout, changed = l, true
		}
	}
	if q.Has("inbox") {
		var inboxes []string
		for _, v := range q["inbox"] {
			if v = strings.TrimSpace(v); v != "" {
				inboxes = append(inboxes, v)
			}
		}
		if strings.Join(inboxes, "\x00") != strings.Join(cur.Inboxes, "\x00") {
			opts.Inboxes, changed = inboxes, true
		}
	}
	return opts, changed, nil
}

// LibraryJSON is the library as the API sends it. Photos are referred to by
// their index in the photos list so that events, folders and suggestions
// stay small.
type LibraryJSON struct {
	Root        string           `json:"root"`
	Home        *homeView        `json:"home"`
	Options     optionsView      `json:"options"`
	Photos      []photoView      `json:"photos"`
	Events      []eventView      `json:"events"`
	Folders     []folderView     `json:"folders"`
	Suggestions []suggestionView `json:"suggestions"`
	Notes       []noteView       `json:"notes"`
	Ignored     int              `json:"ignored"`
}

type homeView struct {
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Inferred bool    `json:"inferred"`
}

type optionsView struct {
	Gap           string   `json:"gap"`
	Jump          string   `json:"jump"`
	HomeRadius    string   `json:"homeRadius"`
	MinTrip       string   `json:"minTrip"`
	MinTripPhotos int      `json:"minTripPhotos"`
	Layout        string   `json:"layout"`
	Inboxes       []string `json:"inboxes"`
	Home          string   `json:"home"`
}

type photoView struct {
	Path    string   `json:"path"`
	Rel     string   `json:"rel"`
	Camera  string   `json:"camera"`
	Format  string   `json:"format"`
	Time    string   `json:"time,omitempty"`
	Lat     *float64 `json:"lat,omitempty"`
	Lon     *float64 `json:"lon,omitempty"`
	Folder  int      `json:"folder"`
	Event   *int     `json:"event"`
	Sidecar bool     `json:"sidecar"`
	Error   string   `json:"error,omitempty"`
}

type eventView struct {
	Index   int            `json:"index"`
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	First   string         `json:"first"`
	Last    string         `json:"last"`
	Days    int            `json:"days"`
	Photos  []int          `json:"photos"`
	Cameras map[string]int `json:"cameras"`
	Located int            `json:"located"`
	Center  *[2]float64    `json:"center,omitempty"`
	Radius  float64        `json:"radius"`
	Away    bool           `json:"away"`
	Folders []folderCount  `json:"folders"`
}

type folderCount struct {
	Folder int `json:"folder"`
	Count  int `json:"count"`
}

type eventCount struct {
	Event int `json:"event"`
	Count int `json:"count"`
}

type folderView struct {
	Index   int          `json:"index"`
	Path    string       `json:"path"`
	Rel     string       `json:"rel"`
	Kind    string       `json:"kind"`
	Note    string       `json:"note"`
	Period  string       `json:"period,omitempty"`
	Photos  []int        `json:"photos"`
	Undated int          `json:"undated"`
	Events  []eventCount `json:"events"`
}

type suggestionView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Event      *int   `json:"event"`
	From       *int   `json:"from"`
	To         string `json:"to"`
	ToRel      string `json:"toRel"`
	Exists     bool   `json:"exists"`
	Photos     []int  `json:"photos"`
	Reason     string `json:"reason"`
	Confidence string `json:"confidence"`
}

type noteView struct {
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Folder *int   `json:"folder"`
	Photos []int  `json:"photos"`
}

// View renders the library as the API sends it, for the command line JSON
// output.
func View(res *app.OrganizeResult) LibraryJSON { return libraryView(res) }

func libraryView(res *app.OrganizeResult) LibraryJSON {
	lib := res.Library
	index := make(map[*camsync.Photo]int, len(res.Photos))
	for i, p := range res.Photos {
		index[p] = i
	}
	folderIndex := make(map[*camsync.Folder]int, len(lib.Folders))
	for i, f := range lib.Folders {
		folderIndex[f] = i
	}
	eventIndex := make(map[*camsync.Event]int, len(lib.Events))
	for i, e := range lib.Events {
		eventIndex[e] = i
	}
	ids := func(photos []*camsync.Photo) []int {
		out := make([]int, 0, len(photos))
		for _, p := range photos {
			out = append(out, index[p])
		}
		return out
	}
	eventOf := map[*camsync.Photo]int{}
	for i, e := range lib.Events {
		for _, p := range e.Photos {
			eventOf[p] = i
		}
	}
	v := LibraryJSON{
		Root: lib.Root, Ignored: res.Ignored,
		Photos: []photoView{}, Events: []eventView{}, Folders: []folderView{}, Suggestions: []suggestionView{}, Notes: []noteView{},
	}
	if lib.Home != nil {
		v.Home = &homeView{Lat: lib.Home.Lat, Lon: lib.Home.Lon, Inferred: lib.HomeInferred}
	}
	v.Options = optionsJSON(res.Options)
	for _, p := range res.Photos {
		rel, err := filepath.Rel(lib.Root, p.Path)
		if err != nil {
			rel = p.Path
		}
		pv := photoView{Path: p.Path, Rel: filepath.ToSlash(rel), Camera: p.Camera, Format: p.Format, Sidecar: p.Sidecar != ""}
		if p.Original != nil {
			pv.Time = p.Original.String()
		}
		if p.Position != nil {
			lat, lon := p.Position.Lat, p.Position.Lon
			pv.Lat, pv.Lon = &lat, &lon
		}
		if f := lib.Folder(filepath.Dir(p.Path)); f != nil {
			pv.Folder = folderIndex[f]
		}
		if e, ok := eventOf[p]; ok {
			pv.Event = &e
		}
		if p.Err != nil {
			pv.Error = p.Err.Error()
		}
		v.Photos = append(v.Photos, pv)
	}
	for _, e := range lib.Events {
		ev := eventView{
			Index: e.Index, Name: e.Name(), Kind: string(e.Kind), First: e.First.Format("2006-01-02 15:04:05"), Last: e.Last.Format("2006-01-02 15:04:05"),
			Days: e.Days(), Photos: ids(e.Photos), Cameras: e.Cameras, Located: e.Located, Radius: e.Radius, Away: e.Away, Folders: []folderCount{},
		}
		if e.Placed() {
			ev.Center = &[2]float64{e.Center.Lat, e.Center.Lon}
		}
		for j, f := range lib.Folders {
			if n := f.Counts[e]; n > 0 {
				ev.Folders = append(ev.Folders, folderCount{Folder: j, Count: n})
			}
		}
		v.Events = append(v.Events, ev)
	}
	for i, f := range lib.Folders {
		fv := folderView{Index: i, Path: f.Path, Rel: filepath.ToSlash(f.Rel), Kind: string(f.Kind), Note: f.Note, Photos: ids(f.Photos), Undated: f.Undated, Events: []eventCount{}}
		if f.Period != nil {
			fv.Period = f.Period.String()
		}
		for _, e := range f.Events {
			fv.Events = append(fv.Events, eventCount{Event: eventIndex[e], Count: f.Counts[e]})
		}
		v.Folders = append(v.Folders, fv)
	}
	for _, s := range lib.Suggestions {
		sv := suggestionView{ID: s.ID, Kind: string(s.Kind), To: s.To, Photos: ids(s.Photos), Reason: s.Reason, Confidence: string(s.Confidence)}
		if s.Event != nil {
			i := eventIndex[s.Event]
			sv.Event = &i
		}
		if s.From != nil {
			i := folderIndex[s.From]
			sv.From = &i
		}
		if s.To != "" {
			rel, err := filepath.Rel(lib.Root, s.To)
			if err != nil {
				rel = s.To
			}
			sv.ToRel = filepath.ToSlash(rel)
			sv.Exists = lib.Folder(s.To) != nil
		}
		v.Suggestions = append(v.Suggestions, sv)
	}
	for _, n := range lib.Notes {
		nv := noteView{Kind: n.Kind, Text: n.Text, Photos: ids(n.Photos)}
		if n.Folder != nil {
			i := folderIndex[n.Folder]
			nv.Folder = &i
		}
		v.Notes = append(v.Notes, nv)
	}
	return v
}

func optionsJSON(o camsync.LibraryOptions) optionsView {
	def := func(d time.Duration, fallback time.Duration) string {
		if d <= 0 {
			d = fallback
		}
		return camsync.FormatSpan(d)
	}
	dist := func(m, fallback float64) string {
		if m <= 0 {
			m = fallback
		}
		return camsync.FormatDistance(m)
	}
	v := optionsView{
		Gap: def(o.MaxGap, camsync.DefaultEventGap), Jump: dist(o.MaxJump, camsync.DefaultEventJump), HomeRadius: dist(o.HomeRadius, camsync.DefaultHomeRadius),
		MinTrip: def(o.MinTripSpan, camsync.DefaultMinTripSpan), MinTripPhotos: o.MinTripPhotos, Layout: string(o.Layout), Inboxes: o.Inboxes,
	}
	if v.MinTripPhotos <= 0 {
		v.MinTripPhotos = camsync.DefaultMinTripPhotos
	}
	if v.Layout == "" {
		v.Layout = string(camsync.LayoutAuto)
	}
	if v.Inboxes == nil {
		v.Inboxes = []string{}
	}
	if o.Home != nil {
		v.Home = o.Home.String()
	}
	return v
}
