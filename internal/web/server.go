// Package web serves the organize user interface: a local HTTP server with
// a JSON API over the application layer and an embedded single-page front
// end. It refuses to leave the library: previews are served for scanned
// photos only and moves are validated against the root before they are
// applied. Requests must name the server by an IP address, localhost or
// the host it was told to listen on, which keeps pages of other sites out
// even when they make their own name resolve to the loopback address.
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

// OSMTiles are the OpenStreetMap tiles, the default map.
const OSMTiles = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"

const osmAttribution = `&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>`

// Config is what the front end is told about its surroundings.
type Config struct {
	// Tiles is the URL template of the map tiles, with {z}, {x} and {y};
	// empty hides the map.
	Tiles string
}

// Server holds one scanned library and answers the front end.
type Server struct {
	mu     sync.RWMutex // guards res, byPath and hosts
	op     sync.Mutex   // one change to the library at a time
	scope  app.Scope
	res    *app.OrganizeResult
	byPath map[string]*camsync.Photo
	hosts  map[string]bool
	cache  *preview.Cache
	cfg    Config
	mux    *http.ServeMux
}

// New wraps a scanned library. cache may be nil, in which case previews
// are rendered on every request.
func New(scope app.Scope, res *app.OrganizeResult, cache *preview.Cache, cfg Config) *Server {
	s := &Server{scope: scope, cache: cache, cfg: cfg, hosts: map[string]bool{}, mux: http.NewServeMux()}
	s.set(res)
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	s.mux.Handle("GET /", http.FileServerFS(sub))
	s.mux.HandleFunc("GET /api/config", s.config)
	s.mux.HandleFunc("GET /api/library", s.library)
	s.mux.HandleFunc("POST /api/plan", s.plan)
	s.mux.HandleFunc("POST /api/rescan", s.rescan)
	s.mux.HandleFunc("GET /api/preview", s.preview)
	s.mux.HandleFunc("POST /api/apply", s.apply)
	s.mux.HandleFunc("GET /api/journal", s.journal)
	s.mux.HandleFunc("POST /api/undo", s.undo)
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.knownHost(r.Host) {
			http.Error(w, "unknown host "+r.Host, http.StatusForbidden)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

// AllowHost lets requests name the server by host, in addition to IP
// addresses and localhost.
func (s *Server) AllowHost(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[strings.ToLower(strings.TrimSuffix(host, "."))] = true
}

// knownHost reports whether a Host header names this server. A page whose
// own name was made to resolve to the loopback address (DNS rebinding)
// sends that name and is refused.
func (s *Server) knownHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host) != nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hosts[host]
}

func (s *Server) set(res *app.OrganizeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.res = res
	s.byPath = make(map[string]*camsync.Photo, len(res.Photos))
	for _, p := range res.Photos {
		s.byPath[p.Path] = p
	}
}

func (s *Server) current() (*app.OrganizeResult, map[string]*camsync.Photo) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.res, s.byPath
}

// Serve listens on addr, tells ready the URL, and serves until ctx is
// cancelled. addr may end in ":0" to pick a free port.
func Serve(ctx context.Context, addr string, s *Server, ready func(url string)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" && net.ParseIP(host) == nil {
		s.AllowHost(host)
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

type configView struct {
	Tiles       string `json:"tiles"`
	Attribution string `json:"attribution"`
}

func (s *Server) config(w http.ResponseWriter, _ *http.Request) {
	v := configView{Tiles: s.cfg.Tiles}
	if s.cfg.Tiles == OSMTiles {
		v.Attribution = osmAttribution
	} else if u, err := url.Parse(s.cfg.Tiles); err == nil && u.Host != "" {
		v.Attribution = "tiles: " + u.Host
	}
	writeJSON(w, v)
}

// library answers with the photos and the current plan.
func (s *Server) library(w http.ResponseWriter, _ *http.Request) {
	res, _ := s.current()
	writeJSON(w, libraryView(res))
}

// plan plans the scanned photos again with the options in the form and
// answers with the plan alone; the photos stay what they were.
func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		writeError(w, fmt.Errorf("bad request: %w", err), http.StatusBadRequest)
		return
	}
	s.op.Lock()
	defer s.op.Unlock()
	res, _ := s.current()
	opts, changed, err := parseOptions(r.PostForm, res.Root, res.Options)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if changed {
		res = res.With(opts)
		s.set(res)
	}
	writeJSON(w, planView(res))
}

// rescan reads the library from disk again with the current options.
func (s *Server) rescan(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	s.op.Lock()
	defer s.op.Unlock()
	res, err := s.scan(r.Context())
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, libraryView(res))
}

// scan reads the library from disk with the current options and keeps it.
// The caller holds s.op.
func (s *Server) scan(ctx context.Context) (*app.OrganizeResult, error) {
	cur, _ := s.current()
	res, err := app.OrganizeHandler{}.Handle(ctx, app.OrganizeQuery{Scope: s.scope, Options: cur.Options})
	if err != nil {
		return nil, err
	}
	s.set(res)
	return res, nil
}

// preview serves a scaled JPEG of a scanned photo.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	_, byPath := s.current()
	p := byPath[path]
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
	Moved        int              `json:"moved"`
	Failed       int              `json:"failed"`
	DryRun       bool             `json:"dryRun"`
	Created      []string         `json:"created"`
	Results      []moveResultView `json:"results"`
	JournalError string           `json:"journalError,omitempty"`
	Undo         *journalView     `json:"undo"`
	Library      *LibraryJSON     `json:"library,omitempty"`
}

type moveResultView struct {
	Path    string `json:"path"`
	To      string `json:"to"`
	NewPath string `json:"newPath"`
	Error   string `json:"error,omitempty"`
}

type journalView struct {
	Time    string `json:"time"`
	Moves   int    `json:"moves"`
	Created int    `json:"created"`
}

func newApplyResponse(res *camsync.ApplyResult) applyResponse {
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
	return out
}

// finish completes the answer to an apply or undo: the journal left to
// undo and, when files moved, the library scanned again.
func (s *Server) finish(ctx context.Context, out *applyResponse, root string) {
	out.Undo = lastJournal(root)
	if out.DryRun || out.Moved == 0 {
		return
	}
	if fresh, err := s.scan(ctx); err == nil {
		lib := libraryView(fresh)
		out.Library = &lib
	}
}

// apply moves the given photos and, unless it was a dry run, rescans. A
// destination may be given relative to the root, with slashes.
func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	var req applyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		writeError(w, fmt.Errorf("bad request: %w", err), http.StatusBadRequest)
		return
	}
	s.op.Lock()
	defer s.op.Unlock()
	res, byPath := s.current()
	// Only photos of this library move, and their sidecars come from the
	// scan rather than from the request.
	moves := make([]camsync.Move, 0, len(req.Moves))
	for _, m := range req.Moves {
		p := byPath[m.Path]
		if p == nil {
			writeError(w, fmt.Errorf("%s is not a photo of this library", m.Path), http.StatusBadRequest)
			return
		}
		to, err := destination(res.Root, m.To)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		moves = append(moves, camsync.Move{Path: p.Path, Sidecar: p.Sidecar, To: to})
	}
	ar, err := app.ApplyHandler{}.Handle(r.Context(), app.ApplyCommand{Plan: camsync.Plan{Root: res.Root, Moves: moves}, DryRun: req.DryRun})
	if err != nil {
		status := http.StatusInternalServerError
		var verr *app.ValidationError
		if errors.As(err, &verr) {
			status = http.StatusBadRequest
		}
		writeError(w, err, status)
		return
	}
	out := newApplyResponse(ar.ApplyResult)
	if ar.JournalErr != nil {
		out.JournalError = ar.JournalErr.Error()
	}
	s.finish(r.Context(), &out, res.Root)
	writeJSON(w, out)
}

// destination resolves a destination directory against the root. Hidden
// directories are refused: scans skip them, so photos moved there would
// disappear from the library.
func destination(root, to string) (string, error) {
	if strings.TrimSpace(to) == "" {
		return "", errors.New("move without destination")
	}
	if !filepath.IsAbs(to) {
		to = filepath.Join(root, filepath.FromSlash(to))
	}
	to = filepath.Clean(to)
	rel, err := filepath.Rel(root, to)
	if err != nil {
		return "", err
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return "", fmt.Errorf("%s: hidden folders are not part of the library", rel)
		}
	}
	return to, nil
}

func lastJournal(root string) *journalView {
	j, _, err := camsync.LastJournal(root)
	if err != nil {
		return nil
	}
	return &journalView{Time: j.Time.Format(time.RFC3339), Moves: len(j.Moves), Created: len(j.Created)}
}

// journal answers with the last applied plan that can be undone, or null.
func (s *Server) journal(w http.ResponseWriter, _ *http.Request) {
	res, _ := s.current()
	writeJSON(w, lastJournal(res.Root))
}

type undoRequest struct {
	DryRun bool `json:"dryRun"`
}

// undo moves the photos of the last applied plan back.
func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	var req undoRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		writeError(w, fmt.Errorf("bad request: %w", err), http.StatusBadRequest)
		return
	}
	s.op.Lock()
	defer s.op.Unlock()
	res, _ := s.current()
	ur, err := app.UndoHandler{}.Handle(r.Context(), app.UndoCommand{Root: res.Root, DryRun: req.DryRun})
	if ur == nil {
		status := http.StatusInternalServerError
		if errors.Is(err, camsync.ErrNothingToUndo) {
			status = http.StatusConflict
		}
		writeError(w, err, status)
		return
	}
	out := newApplyResponse(ur.ApplyResult)
	if err != nil {
		out.JournalError = err.Error()
	}
	s.finish(r.Context(), &out, res.Root)
	writeJSON(w, out)
}

// parseOptions reads the planning options from a form, starting from the
// current ones. It reports whether anything changed.
func parseOptions(q url.Values, root string, cur camsync.LibraryOptions) (camsync.LibraryOptions, bool, error) {
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
			if v = strings.TrimSpace(v); v == "" {
				continue
			}
			if !filepath.IsAbs(v) {
				v = filepath.Join(root, filepath.FromSlash(v))
			}
			inboxes = append(inboxes, filepath.Clean(v))
		}
		if strings.Join(inboxes, "\x00") != strings.Join(cur.Inboxes, "\x00") {
			opts.Inboxes, changed = inboxes, true
		}
	}
	return opts, changed, nil
}

// PlanJSON is what planning the photos yields, as the API sends it. Photos
// are referred to by their index in the photos list of the library so that
// events, folders and suggestions stay small, and so that replanning need
// not send the photos again.
type PlanJSON struct {
	Root        string           `json:"root"`
	Home        *homeView        `json:"home"`
	Options     optionsView      `json:"options"`
	Events      []eventView      `json:"events"`
	Folders     []folderView     `json:"folders"`
	Suggestions []suggestionView `json:"suggestions"`
	Notes       []noteView       `json:"notes"`
	Ignored     int              `json:"ignored"`
}

// LibraryJSON is the library as the API sends it: the plan and the photos.
type LibraryJSON struct {
	PlanJSON
	Photos []photoView `json:"photos"`
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
	Inboxes       []string `json:"inboxes"` // relative to the root, with slashes
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

func slashRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func libraryView(res *app.OrganizeResult) LibraryJSON {
	lib := res.Library
	v := LibraryJSON{PlanJSON: planView(res), Photos: []photoView{}}
	folderIndex := make(map[string]int, len(lib.Folders))
	for i, f := range lib.Folders {
		folderIndex[f.Path] = i
	}
	eventOf := map[*camsync.Photo]int{}
	for i, e := range lib.Events {
		for _, p := range e.Photos {
			eventOf[p] = i
		}
	}
	for _, p := range res.Photos {
		pv := photoView{Path: p.Path, Rel: slashRel(lib.Root, p.Path), Camera: p.Camera, Format: p.Format, Sidecar: p.Sidecar != ""}
		if p.Original != nil {
			pv.Time = p.Original.String()
		}
		if p.Position != nil {
			lat, lon := p.Position.Lat, p.Position.Lon
			pv.Lat, pv.Lon = &lat, &lon
		}
		pv.Folder = folderIndex[filepath.Dir(p.Path)]
		if e, ok := eventOf[p]; ok {
			pv.Event = &e
		}
		if p.Err != nil {
			pv.Error = p.Err.Error()
		}
		v.Photos = append(v.Photos, pv)
	}
	return v
}

func planView(res *app.OrganizeResult) PlanJSON {
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
	v := PlanJSON{
		Root: lib.Root, Ignored: res.Ignored,
		Events: []eventView{}, Folders: []folderView{}, Suggestions: []suggestionView{}, Notes: []noteView{},
	}
	if lib.Home != nil {
		v.Home = &homeView{Lat: lib.Home.Lat, Lon: lib.Home.Lon, Inferred: lib.HomeInferred}
	}
	v.Options = optionsJSON(lib.Root, res.Options)
	for _, e := range lib.Events {
		ev := eventView{
			Index: e.Index, Name: e.Name(), Kind: string(e.Kind), First: e.First.Format("2006-01-02 15:04:05"), Last: e.Last.Format("2006-01-02 15:04:05"),
			Days: e.Days(), Photos: ids(e.Photos), Cameras: e.Cameras, Located: e.Located, Radius: e.Radius, Away: e.Away, Folders: []folderCount{},
		}
		if e.Placed() {
			ev.Center = &[2]float64{e.Center.Lat, e.Center.Lon}
		}
		v.Events = append(v.Events, ev)
	}
	for i, f := range lib.Folders {
		fv := folderView{Index: i, Path: f.Path, Rel: filepath.ToSlash(f.Rel), Kind: string(f.Kind), Note: f.Note, Photos: ids(f.Photos), Undated: f.Undated, Events: []eventCount{}}
		if f.Period != nil {
			fv.Period = f.Period.String()
		}
		for _, e := range f.Events {
			j := eventIndex[e]
			fv.Events = append(fv.Events, eventCount{Event: j, Count: f.Counts[e]})
			v.Events[j].Folders = append(v.Events[j].Folders, folderCount{Folder: i, Count: f.Counts[e]})
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
			sv.ToRel = slashRel(lib.Root, s.To)
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

func optionsJSON(root string, o camsync.LibraryOptions) optionsView {
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
		MinTrip: def(o.MinTripSpan, camsync.DefaultMinTripSpan), MinTripPhotos: o.MinTripPhotos, Layout: string(o.Layout), Inboxes: []string{},
	}
	if v.MinTripPhotos <= 0 {
		v.MinTripPhotos = camsync.DefaultMinTripPhotos
	}
	if v.Layout == "" {
		v.Layout = string(camsync.LayoutAuto)
	}
	for _, in := range o.Inboxes {
		v.Inboxes = append(v.Inboxes, slashRel(root, in))
	}
	if o.Home != nil {
		v.Home = o.Home.String()
	}
	return v
}
