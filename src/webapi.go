package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The HTTP front door.
//
// Loopback only, and still authenticated: "bound to 127.0.0.1" does not mean
// "only this user" on a shared machine, and a local web server that can spawn
// yt-dlp on request is a bigger footgun than one that renders a page. The token
// is minted per run and exchanged for a SameSite cookie on the first navigation,
// so the URL in shell history does not stay a working credential.
//
// No third-party router, no WebSocket library, no template engine. The UI is a
// static embed and the protocol is five bytes of framing.

const (
	webCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https://i.ytimg.com https://img.youtube.com https://*.ytimg.com; " +
		"media-src 'self' blob:; connect-src 'self'; font-src 'self'; worker-src 'self'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	mediaUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

type webServer struct {
	eng   *webEngine
	token string
	host  string // hostname the listener actually bound to
	port  int

	assets http.Handler

	searchMu sync.Mutex
	mux      *http.ServeMux
}

func assetRoot() fs.FS {
	root, err := fs.Sub(webAssets, "assets")
	if err != nil {
		panic("embed assets: " + err.Error())
	}
	return root
}

func newWebServer(eng *webEngine, token, host string, port int) *webServer {
	s := &webServer{
		eng:    eng,
		token:  token,
		host:   host,
		port:   port,
		assets: http.FileServer(http.FS(assetRoot())),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", s.assets))
	mux.HandleFunc("GET /manifest.webmanifest", s.manifest)
	mux.HandleFunc("GET /sw.js", s.serviceWorker)
	mux.HandleFunc("GET /api/state", s.apiState)
	mux.HandleFunc("GET /api/events", s.apiEvents)
	mux.HandleFunc("GET /api/roots", s.apiRoots)
	mux.HandleFunc("GET /api/settings", s.apiSettingsGet)
	mux.HandleFunc("GET /api/version", s.apiVersion)
	mux.HandleFunc("GET /api/media/{token}", s.apiMedia)
	mux.HandleFunc("POST /api/search", s.apiSearch)
	mux.HandleFunc("POST /api/library", s.apiLibrary)
	mux.HandleFunc("POST /api/pick-folder", s.apiPickFolder)
	mux.HandleFunc("POST /api/queue", s.apiQueue)
	mux.HandleFunc("POST /api/queue/add", s.apiQueueAdd)
	mux.HandleFunc("POST /api/play", s.apiPlay)
	mux.HandleFunc("POST /api/transport", s.apiTransport)
	mux.HandleFunc("POST /api/report", s.apiReport)
	mux.HandleFunc("POST /api/viz", s.apiViz)
	mux.HandleFunc("POST /api/viewport", s.apiViewport)
	mux.HandleFunc("POST /api/mode", s.apiMode)
	mux.HandleFunc("POST /api/download", s.apiDownload)
	mux.HandleFunc("POST /api/settings", s.apiSettingsSet)
	mux.HandleFunc("POST /api/update", s.apiUpdate)
	s.mux = mux
	return s
}

func (s *webServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if !s.hostAllowed(r.Host) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	// First navigation carries the token; exchange it for a cookie and drop it
	// from the URL so a screenshot or a copied address bar is not a credential.
	if r.URL.Path == "/" && r.URL.Query().Get("token") != "" {
		if !s.tokenEqual(r.URL.Query().Get("token")) {
			http.Error(w, "bad token", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "rb_session",
			Value:    s.token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   365 * 24 * 60 * 60,
		})
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "open the URL printed by `rasterbar web`", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.originAllowed(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *webServer) tokenEqual(v string) bool {
	return subtle.ConstantTimeCompare([]byte(v), []byte(s.token)) == 1
}

func (s *webServer) authorized(r *http.Request) bool {
	if s.tokenEqual(r.Header.Get("X-RasterBar-Token")) {
		return true
	}
	c, err := r.Cookie("rb_session")
	if err != nil {
		return false
	}
	return s.tokenEqual(c.Value)
}

func hostOnly(h string) string {
	host, _, err := net.SplitHostPort(h)
	if err != nil {
		host = h
	}
	return strings.Trim(strings.ToLower(host), "[]")
}

func (s *webServer) hostAllowed(h string) bool {
	host := hostOnly(h)
	switch host {
	case "", "127.0.0.1", "localhost", "::1":
		return true
	}
	if s.host != "" && host == strings.ToLower(s.host) {
		return true
	}
	return false
}

func (s *webServer) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// curl and same-origin navigations. A browser fetch() POST sends it, so
		// an empty Origin here is not a browser CSRF vector.
		return true
	}
	u, err := parseOrigin(origin)
	if err != nil {
		return false
	}
	return s.hostAllowed(u.host)
}

type originInfo struct{ host string }

func parseOrigin(raw string) (originInfo, error) {
	rest, ok := strings.CutPrefix(raw, "http://")
	if !ok {
		if rest2, ok2 := strings.CutPrefix(raw, "https://"); ok2 {
			rest = rest2
		} else {
			return originInfo{}, errors.New("bad origin")
		}
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return originInfo{}, errors.New("bad origin")
	}
	return originInfo{host: rest}, nil
}

func (s *webServer) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(webAssets, "index.html")
	if err != nil {
		http.Error(w, "embedded UI missing: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", webCSP)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *webServer) manifest(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(webAssets, "manifest.webmanifest")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *webServer) serviceWorker(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(webAssets, "sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Service-Worker-Allowed", "/")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad json: %w", err))
		return false
	}
	return true
}

func (s *webServer) apiState(w http.ResponseWriter, r *http.Request) {
	st, _ := s.eng.State()
	writeJSON(w, http.StatusOK, st)
}

func (s *webServer) apiEvents(w http.ResponseWriter, r *http.Request) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	c := s.eng.AddClient()
	defer s.eng.RemoveClient(c)

	var last uint64 = ^uint64(0)
	sendState := func() bool {
		st, gen := s.eng.State()
		if gen == last {
			return true
		}
		last = gen
		p, err := webStatePacket(st)
		if err != nil {
			return false
		}
		_, err = w.Write(p)
		return err == nil
	}
	if !sendState() {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}

	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case p := <-c.frames:
			if _, err := w.Write(p); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-tick.C:
			if !sendState() {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func (s *webServer) apiSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"q"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	q := strings.TrimSpace(req.Query)
	if q == "" {
		writeErr(w, http.StatusBadRequest, errors.New("empty query"))
		return
	}
	if len(q) > 300 {
		writeErr(w, http.StatusBadRequest, errors.New("query too long"))
		return
	}
	// One ytfzf/yt-dlp scrape at a time. Two concurrent PTY scrapers on one
	// machine is a way to get rate-limited rather than a way to get answers
	// faster, and the UI submits on a click, not on every keystroke.
	s.searchMu.Lock()
	tracks, err := Search(q)
	s.searchMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tracks": tracks})
}

func (s *webServer) apiLibrary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir   string `json:"dir"`
		Probe bool   `json:"probe"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	dir := expandTilde(strings.TrimSpace(req.Dir))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("no home directory"))
			return
		}
		dir = home
	}
	tracks, err := ScanLibrary(dir, req.Probe)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	truncated := false
	if len(tracks) > webMaxTracks {
		tracks = tracks[:webMaxTracks]
		truncated = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tracks": tracks, "dir": dir, "truncated": truncated,
	})
}

func (s *webServer) apiRoots(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	type root struct {
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}
	var out []root
	add := func(name, dir string) {
		if dir == "" {
			return
		}
		dir = expandTilde(dir)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			out = append(out, root{Name: name, Dir: dir})
		}
	}
	add("Home", home)
	add("Music", filepath.Join(home, "Music"))
	add("Videos", filepath.Join(home, "Videos"))
	add("Downloads", filepath.Join(home, "Downloads"))
	add("Downloads cache", s.eng.CacheDir())
	writeJSON(w, http.StatusOK, map[string]any{"roots": out})
}

func (s *webServer) apiPickFolder(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	if path, err := exec.LookPath("zenity"); err == nil {
		out, err := exec.CommandContext(ctx, path,
			"--file-selection", "--directory", "--title=Choose a folder").Output()
		if err == nil {
			dir := strings.TrimSpace(string(out))
			if dir != "" {
				writeJSON(w, http.StatusOK, map[string]string{"dir": dir})
				return
			}
		}
	}
	if path, err := exec.LookPath("kdialog"); err == nil {
		out, err := exec.CommandContext(ctx, path,
			"--getexistingdirectory", ".").Output()
		if err == nil {
			dir := strings.TrimSpace(string(out))
			if dir != "" {
				writeJSON(w, http.StatusOK, map[string]string{"dir": dir})
				return
			}
		}
	}
	writeErr(w, http.StatusNotImplemented,
		errors.New("no folder picker available (install zenity or kdialog), type the path instead"))
}

// normalizeTrack folds the shapes a browser can send into the one the player
// expects. A local file sometimes arrives with its path in `url` (no scheme),
// and a pasted link can arrive without a scheme; sorting it out here means
// resolveMedia never sees a half-labelled track.
func normalizeTrack(t *Track) {
	if t.URL == "" {
		return
	}
	if strings.HasPrefix(t.URL, "file://") {
		u, err := url.Parse(t.URL)
		if err == nil && u.Path != "" {
			if t.LocalPath == "" {
				t.LocalPath = filepath.Clean(u.Path)
			}
			t.URL = ""
		}
		return
	}
	if filepath.IsAbs(t.URL) && !strings.Contains(t.URL, "://") {
		if t.LocalPath == "" {
			t.LocalPath = t.URL
		}
		t.URL = ""
	}
}

func validateTracks(tracks []Track) error {
	for i := range tracks {
		normalizeTrack(&tracks[i])
		t := tracks[i]
		if len(t.Title) > 500 {
			return fmt.Errorf("track %d: title too long", i)
		}
		if t.URL != "" {
			if !strings.HasPrefix(t.URL, "http://") && !strings.HasPrefix(t.URL, "https://") &&
				!strings.HasPrefix(t.URL, "ftp://") {
				return fmt.Errorf("track %d: unsupported url %q (expected http, https, ftp or a local path)", i, t.URL)
			}
		}
		if t.LocalPath != "" && !filepath.IsAbs(t.LocalPath) {
			return fmt.Errorf("track %d: local path must be absolute", i)
		}
		if t.URL == "" && t.LocalPath == "" {
			return fmt.Errorf("track %d: nothing to play (no url and no local path)", i)
		}
	}
	return nil
}

func (s *webServer) apiQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tracks []Track `json:"tracks"`
		Index  int     `json:"index"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Tracks) == 0 {
		s.eng.Clear()
		writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
		return
	}
	if len(req.Tracks) > webMaxTracks {
		writeErr(w, http.StatusBadRequest,
			fmt.Errorf("too many tracks (%d, max %d)", len(req.Tracks), webMaxTracks))
		return
	}
	if err := validateTracks(req.Tracks); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.eng.Queue(req.Tracks, req.Index)
	writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
}

func (s *webServer) apiQueueAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tracks []Track `json:"tracks"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Tracks) > webMaxTracks {
		writeErr(w, http.StatusBadRequest, errors.New("too many tracks"))
		return
	}
	if err := validateTracks(req.Tracks); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.eng.AddTracks(req.Tracks)
	writeJSON(w, http.StatusOK, map[string]string{"status": "added"})
}

func (s *webServer) apiPlay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int `json:"index"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.eng.Play(req.Index)
	writeJSON(w, http.StatusOK, map[string]string{"status": "playing"})
}

func (s *webServer) apiTransport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string  `json:"action"`
		Value  float64 `json:"value"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	switch req.Action {
	case "pause":
		s.eng.SetPaused(true)
	case "play":
		s.eng.SetPaused(false)
	case "toggle":
		s.eng.TogglePause()
	case "next":
		s.eng.Next()
	case "prev":
		s.eng.Prev()
	case "seek":
		s.eng.Seek(req.Value)
	case "volume":
		s.eng.SetVolume(int(req.Value))
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown action %q", req.Action))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": req.Action})
}

func (s *webServer) apiReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Position float64 `json:"position"`
		Duration float64 `json:"duration"`
		Ended    bool    `json:"ended"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.eng.Report(req.Position, req.Duration, req.Ended)
	w.WriteHeader(http.StatusNoContent)
}

func (s *webServer) apiViz(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Style   string `json:"style"`
		Palette string `json:"palette"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Style != "" {
		if err := s.eng.SetViz(req.Style); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if req.Palette != "" {
		if err := s.eng.SetPalette(req.Palette); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *webServer) apiViewport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.eng.SetViewport(req.Cols, req.Rows)
	w.WriteHeader(http.StatusNoContent)
}

func (s *webServer) apiMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Video *bool `json:"video"`
		Split *bool `json:"split"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Video != nil {
		s.eng.SetVideo(*req.Video)
	}
	if req.Split != nil {
		s.eng.SetSplit(*req.Split)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *webServer) apiDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int    `json:"index"`
		Track *Track `json:"track"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var err error
	if req.Track != nil {
		err = s.eng.DownloadTrack(*req.Track)
	} else {
		err = s.eng.Download(req.Index)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "downloading"})
}

func (s *webServer) apiSettingsGet(w http.ResponseWriter, r *http.Request) {
	e := s.eng
	e.mu.Lock()
	cfg := e.cfg
	e.mu.Unlock()
	writeJSON(w, http.StatusOK, cfg)
}

func (s *webServer) apiSettingsSet(w http.ResponseWriter, r *http.Request) {
	var req settings
	if !readJSON(w, r, &req) {
		return
	}
	validated, err := req.validate()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.UpdateSettings(validated); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if validated.CookiesFrom != "" {
		ytdlpAuth = ytdlpAuthConfig{FromBrowser: validated.CookiesFrom}
	}
	writeJSON(w, http.StatusOK, validated)
}

func (s *webServer) apiVersion(w http.ResponseWriter, r *http.Request) {
	v := version
	if v == "" {
		v = "development"
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": v})
}

func (s *webServer) apiUpdate(w http.ResponseWriter, r *http.Request) {
	msg, err := runUpdate(true)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// mediaClient has no overall timeout on purpose: an audio stream can run for an
// hour. ResponseHeaderTimeout bounds the part that can hang invisibly.
var mediaClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          8,
	},
}

func (s *webServer) fetchMedia(ctx context.Context, r *http.Request, raw string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, r.Method, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", mediaUA)
	req.Header.Set("Referer", "https://www.youtube.com/")
	req.Header.Set("Origin", "https://www.youtube.com")
	if v := r.Header.Get("Range"); v != "" {
		req.Header.Set("Range", v)
	}
	if v := r.Header.Get("If-Range"); v != "" {
		req.Header.Set("If-Range", v)
	}
	return mediaClient.Do(req)
}

func (s *webServer) apiMedia(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	raw, ok := s.eng.MediaURL(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// A local track's "audio URL" is a filesystem path, not a grant. Serving it
	// through the HTTP client would ask net/http to fetch /home/..., which fails
	// before the audio element ever sees a byte. ServeFile also gives us Range,
	// HEAD and Content-Type for free, which is exactly what <audio> needs.
	if st, err := os.Stat(raw); err == nil && !st.IsDir() {
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, raw)
		return
	}
	resp, err := s.fetchMedia(r.Context(), r, raw)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone {
		// The grant expired (a long pause, a slow seek). Re-mint once. A second
		// failure is reported, because retrying a 403 forever is a spinner with
		// no explanation.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		fresh, ok := s.eng.RefreshMedia(token)
		if !ok {
			writeErr(w, http.StatusBadGateway,
				errors.New("the media link expired and could not be refreshed; press reload"))
			return
		}
		resp, err = s.fetchMedia(r.Context(), r, fresh)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
	}
	defer resp.Body.Close()

	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}
