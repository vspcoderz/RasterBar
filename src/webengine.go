package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// webEngine is the headless player the web UI drives.
//
// It is a sibling of trackSession, not a branch inside it. The TUI's loop owns a
// terminal, a raw-mode key reader and two renderers, none of which exist in a
// browser. What is shared is everything below that: the resolvers, the level tap,
// the video pane, the visualiser registry and the analysis pipeline. The web pump
// feeds the same VizGrid and forwards the same frame buffer the colour renderer
// would have consumed, so the picture is the same picture.
//
// The browser owns the clock. Audio comes from <audio>, and the engine restarts
// its taps with -ss on seeks exactly as the terminal does. Pause is SIGSTOP on
// every child clock, for the same reason: a tap left running through a pause
// resumes out of step with the sound and cannot be corrected (see AGENTS.md).

const (
	webDefaultCols = 180
	webDefaultRows = 50
	webMaxTracks   = 500
)

type mediaEntry struct {
	url string
	gen uint64
}

type webTrack struct {
	Track
	pair     mediaPair
	audioSrc string
	paneURL  string // lazily resolved video URL for a music-mode split pane
}

type webEngine struct {
	mu sync.Mutex

	opts     options
	cfg      settings
	cacheDir string
	mode     ColorMode
	glyph    GlyphMode
	version  string

	queue    []Track
	index    int
	cur      webTrack
	hasTrack bool

	prefs         *vizPrefs
	viz           Viz
	grid          *VizGrid
	cols          int
	rows          int
	paletteColors map[string][]string

	tap    *LevelTap
	pane   *videoPane
	tapGen uint64

	video  bool
	split  bool
	paused bool
	volume int
	pos    float64
	dur    float64

	loading bool
	status  string
	errMsg  string

	gen      uint64
	stateGen uint64
	media    map[string]mediaEntry
	clients  map[*webClient]struct{}

	downloads map[string]string
	dlMu      sync.Mutex

	stop   chan struct{}
	closed bool
	pumpWG sync.WaitGroup
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func clampGrid(cols, rows int) (int, int) {
	if cols < 40 {
		cols = 40
	}
	if cols > 240 {
		cols = 240
	}
	if rows < 20 {
		rows = 20
	}
	if rows > 140 {
		rows = 140
	}
	return cols, rows
}

func newWebEngine(opts options, cfg settings, cacheDir string, mode ColorMode, ver string) *webEngine {
	glyph := GlyphHalf
	if mode == ColorNone || opts.glyph == GlyphCell {
		glyph = GlyphCell
	}
	cols, rows := webDefaultCols, webDefaultRows
	if opts.cols > 0 {
		cols = opts.cols
	}
	if opts.rows > 0 {
		rows = opts.rows
	}
	cols, rows = clampGrid(cols, rows)

	prefs := opts.prefs()
	e := &webEngine{
		opts:          opts,
		cfg:           cfg,
		cacheDir:      cacheDir,
		mode:          mode,
		glyph:         glyph,
		version:       ver,
		prefs:         prefs,
		cols:          cols,
		rows:          rows,
		volume:        80,
		media:         map[string]mediaEntry{},
		clients:       map[*webClient]struct{}{},
		downloads:     map[string]string{},
		paletteColors: palettePreviews(),
		stop:          make(chan struct{}),
		video:         opts.ascii && !opts.music,
	}
	e.viz = prefs.makeViz()
	e.viz.Resize(cols, rows)
	e.grid = NewVizGrid(cols, rows, mode != ColorNone, perCellFor(mode, glyph))
	e.grid.SetPalette(paletteAt(prefs.palette))
	e.pumpWG.Add(1)
	go e.pump()
	return e
}

func (e *webEngine) CacheDir() string { return e.cacheDir }

func (e *webEngine) bumpLocked() { e.stateGen++ }

func (e *webEngine) pump() {
	defer e.pumpWG.Done()
	tick := time.NewTicker(time.Second / 30)
	defer tick.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-tick.C:
			e.frameTick()
		}
	}
}

func (e *webEngine) frameTick() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	var packets [][]byte
	if e.tap != nil {
		if err := e.tap.Dead(); err != nil {
			e.errMsg = err.Error()
			e.status = ""
			e.tap = nil
			e.bumpLocked()
		} else if f, ok := e.tap.TryFrameAt(&e.tapGen); ok {
			e.viz.Push(&f)
			e.grid.Clear()
			e.viz.Paint(e.grid)
			var frame []byte
			if e.mode == ColorNone {
				frame = e.grid.MonoFrame()
			} else {
				frame = e.grid.ColorFrame()
			}
			packets = append(packets, webFramePacket(webPaneViz, e.mode, e.glyph, e.cols, e.rows, frame))
		}
	}
	if e.pane != nil {
		if fr := e.pane.Latest(); len(fr) > 0 {
			packets = append(packets, webFramePacket(webPaneVideo, e.mode, e.glyph, e.cols, e.rows, fr))
		}
	}
	clients := make([]*webClient, 0, len(e.clients))
	for c := range e.clients {
		clients = append(clients, c)
	}
	e.mu.Unlock()
	if len(packets) == 0 || len(clients) == 0 {
		return
	}
	for _, c := range clients {
		for _, p := range packets {
			c.pushFrame(p)
		}
	}
}

// State returns a snapshot plus the generation it was built from. The events
// handler sends a state packet only when that generation changes.
func (e *webEngine) State() (webState, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stateLocked(), e.stateGen
}

func (e *webEngine) stateLocked() webState {
	q := make([]webQueueItem, len(e.queue))
	for i := range e.queue {
		q[i] = queueItem(e.queue[i])
	}
	dl := make(map[string]string, len(e.downloads))
	for k, v := range e.downloads {
		dl[k] = v
	}
	st := webState{
		Version:       e.version,
		Ramp:          ramp,
		Styles:        VizNames(),
		Palettes:      paletteNames(),
		Queue:         q,
		Index:         e.index,
		Has:           e.hasTrack,
		Paused:        e.paused,
		Volume:        e.volume,
		Music:         !e.video,
		Video:         e.video,
		Split:         e.split,
		Loading:       e.loading,
		Status:        e.status,
		Error:         e.errMsg,
		Cols:          e.cols,
		Rows:          e.rows,
		Viz:           e.prefs.StyleName(),
		Palette:       e.prefs.PaletteName(),
		PaletteColors: e.paletteColors,
		CacheDir:      e.cacheDir,
		Settings:      e.cfg,
		Downloading:   dl,
	}
	if e.hasTrack {
		st.Title = e.cur.Title
		st.Channel = e.cur.ChannelText()
		st.Dur = e.dur
		st.Pos = e.pos
		st.Audio = e.cur.audioSrc
		st.Thumb = e.cur.Thumbs
		st.TrackID = e.cur.ID
		st.TrackURL = e.cur.URL
		st.Local = e.cur.IsLocal()
	}
	return st
}

func (e *webEngine) StatePacket() ([]byte, error) {
	e.mu.Lock()
	st := e.stateLocked()
	e.mu.Unlock()
	return webStatePacket(st)
}

func (e *webEngine) AddClient() *webClient {
	c := newWebClient()
	e.mu.Lock()
	e.clients[c] = struct{}{}
	e.mu.Unlock()
	return c
}

func (e *webEngine) RemoveClient(c *webClient) {
	e.mu.Lock()
	delete(e.clients, c)
	e.mu.Unlock()
	c.close()
}

func closeWebMedia(lt *LevelTap, pane *videoPane) {
	if lt != nil {
		lt.Close()
	}
	if pane != nil {
		pane.Close()
	}
}

// Queue replaces the play queue and starts at index.
func (e *webEngine) Queue(tracks []Track, index int) {
	if len(tracks) > webMaxTracks {
		tracks = tracks[:webMaxTracks]
	}
	if len(tracks) == 0 {
		e.Clear()
		return
	}
	// Cached tracks are local from here on: no yt-dlp, free seek, and playback
	// works with the network off. Mutating the caller's tracks is deliberate --
	// the queue the UI sees and the queue playback uses must be the same slice.
	serveFromCache(e.cacheDir, tracks)
	if index < 0 || index >= len(tracks) {
		index = 0
	}
	e.mu.Lock()
	e.queue = tracks
	e.index = index
	e.mu.Unlock()
	e.load(index, 0)
}

// AddTracks appends to the queue, starting the first one if nothing is playing.
func (e *webEngine) AddTracks(tracks []Track) {
	if len(tracks) == 0 {
		return
	}
	serveFromCache(e.cacheDir, tracks)
	e.mu.Lock()
	wasEmpty := !e.hasTrack
	room := webMaxTracks - len(e.queue)
	if room <= 0 {
		e.mu.Unlock()
		return
	}
	if len(tracks) > room {
		tracks = tracks[:room]
	}
	e.queue = append(e.queue, tracks...)
	e.bumpLocked()
	idx := len(e.queue) - len(tracks)
	e.mu.Unlock()
	if wasEmpty {
		e.load(idx, 0)
	}
}

func (e *webEngine) Clear() {
	e.mu.Lock()
	e.gen++
	t, p := e.tap, e.pane
	e.tap, e.pane = nil, nil
	e.queue = nil
	e.index = 0
	e.cur = webTrack{}
	e.hasTrack = false
	e.loading = false
	e.paused = false
	e.pos, e.dur = 0, 0
	e.status = ""
	e.errMsg = ""
	e.media = map[string]mediaEntry{}
	e.bumpLocked()
	e.mu.Unlock()
	closeWebMedia(t, p)
}

func (e *webEngine) load(index int, at float64) {
	e.mu.Lock()
	if index < 0 || index >= len(e.queue) {
		e.mu.Unlock()
		e.Clear()
		return
	}
	e.gen++
	myGen := e.gen
	oldTap, oldPane := e.tap, e.pane
	e.tap, e.pane = nil, nil
	e.tapGen = 0
	e.cur = webTrack{Track: e.queue[index]}
	e.index = index
	e.hasTrack = true
	e.loading = true
	e.status = ""
	e.errMsg = ""
	e.pos = at
	e.dur = 0
	e.paused = false
	e.media = map[string]mediaEntry{}
	e.bumpLocked()
	track := e.cur.Track
	video, split := e.video, e.split
	cols := e.cols
	e.mu.Unlock()

	closeWebMedia(oldTap, oldPane)
	go func() {
		pair, err := e.resolveTrack(track, video, split, cols)
		if err != nil {
			e.failLoading(myGen, err)
			return
		}
		if video && pair.videoLess {
			// An audio-only local file plays as a visualiser, exactly as the TUI
			// does. The alternative is a video tap on `-map 0:v:0` that fails
			// with "Stream map matches no streams" and ends the track.
			video = false
		}
		lt, pane, err := e.startMedia(pair, video, split, at)
		if err != nil {
			e.failLoading(myGen, err)
			return
		}
		e.mu.Lock()
		if e.gen != myGen || e.closed {
			e.mu.Unlock()
			closeWebMedia(lt, pane)
			return
		}
		token := newToken()
		e.media[token] = mediaEntry{url: pair.audioURL, gen: myGen}
		e.cur.pair = pair
		e.cur.audioSrc = "/api/media/" + token
		e.video = video
		e.tap = lt
		e.pane = pane
		e.dur = pair.dur
		e.loading = false
		e.status = "playing"
		paused := e.paused
		e.bumpLocked()
		e.mu.Unlock()

		if paused {
			if lt != nil {
				lt.SetPaused(true)
			}
			if pane != nil {
				pane.SetPaused(true)
			}
		}
		if split && !video {
			e.ensurePane(myGen)
		}
	}()
}

func (e *webEngine) resolveTrack(track Track, video, split bool, cols int) (mediaPair, error) {
	if video {
		e.mu.Lock()
		quality := e.cfg.Quality
		e.mu.Unlock()
		h := sourceHForCols(cols)
		if quality > 0 {
			h = quality
		}
		return resolveMedia(track, h, split)
	}
	return resolveAudioPair(track)
}

func (e *webEngine) startMedia(pair mediaPair, video, split bool, at float64) (*LevelTap, *videoPane, error) {
	e.mu.Lock()
	cols, rows, mode, glyph := e.cols, e.rows, e.mode, e.glyph
	e.mu.Unlock()

	var lt *LevelTap
	var pane *videoPane
	if video {
		if pair.videoURL == "" {
			return nil, nil, fmt.Errorf("no video stream for video mode")
		}
		pane = newVideoPane(cols, rows, mode, glyph)
		if err := pane.startLive(pair.videoURL, 24, at, mode, glyph); err != nil {
			pane.Close()
			return nil, nil, fmt.Errorf("video: %w", err)
		}
	}
	if !video || split {
		if pair.tapURL == "" {
			if pane != nil {
				pane.Close()
			}
			return nil, nil, fmt.Errorf("no audio stream for the visualiser")
		}
		var err error
		lt, err = StartLevelTapAt(pair.tapURL, at)
		if err != nil {
			if pane != nil {
				pane.Close()
			}
			return nil, nil, fmt.Errorf("visualiser: %w", err)
		}
	}
	return lt, pane, nil
}

func (e *webEngine) failLoading(gen uint64, err error) {
	e.mu.Lock()
	if e.gen == gen && !e.closed {
		e.loading = false
		e.errMsg = err.Error()
		e.status = ""
		e.bumpLocked()
	}
	e.mu.Unlock()
}

func (e *webEngine) failStatus(gen uint64, err error) {
	e.mu.Lock()
	if e.gen == gen && !e.closed {
		e.status = err.Error()
		e.bumpLocked()
	}
	e.mu.Unlock()
}

func (e *webEngine) Play(index int) { e.load(index, 0) }

func (e *webEngine) Next() {
	e.mu.Lock()
	next := e.index + 1
	length := len(e.queue)
	e.mu.Unlock()
	if next < length {
		e.load(next, 0)
		return
	}
	e.finishQueue()
}

func (e *webEngine) Prev() {
	e.mu.Lock()
	prev := e.index - 1
	length := len(e.queue)
	e.mu.Unlock()
	if prev >= 0 && length > 0 {
		e.load(prev, 0)
	}
}

func (e *webEngine) finishQueue() {
	e.mu.Lock()
	e.gen++
	t, p := e.tap, e.pane
	e.tap, e.pane = nil, nil
	e.paused = true
	e.loading = false
	e.status = "end of queue"
	e.pos = e.dur
	e.bumpLocked()
	e.mu.Unlock()
	closeWebMedia(t, p)
}

func (e *webEngine) SetPaused(paused bool) {
	e.mu.Lock()
	e.paused = paused
	lt, pane := e.tap, e.pane
	e.bumpLocked()
	e.mu.Unlock()
	if lt != nil {
		lt.SetPaused(paused)
	}
	if pane != nil {
		pane.SetPaused(paused)
	}
}

func (e *webEngine) TogglePause() {
	e.mu.Lock()
	p := e.paused
	e.mu.Unlock()
	e.SetPaused(!p)
}

func (e *webEngine) SetVolume(v int) {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	e.mu.Lock()
	e.volume = v
	e.bumpLocked()
	e.mu.Unlock()
}

func (e *webEngine) Seek(pos float64) {
	e.mu.Lock()
	if e.dur > 0 && pos > e.dur {
		pos = e.dur
	}
	if pos < 0 {
		pos = 0
	}
	e.pos = pos
	e.bumpLocked()
	ok := e.hasTrack && !e.loading && !e.closed
	e.mu.Unlock()
	if ok {
		go e.restartMedia(pos)
	}
}

// restartMedia rebuilds every child clock at a new offset.
//
// Rebuilding rather than seeking because the tap and the pane each own their own
// ffmpeg and their own grant; there is no shared position to nudge. This is the
// same reason the TUI retires a decoder generation on every seek.
func (e *webEngine) restartMedia(at float64) {
	e.mu.Lock()
	if !e.hasTrack || e.loading || e.closed {
		e.mu.Unlock()
		return
	}
	gen := e.gen
	pair := e.cur.pair
	video, split := e.video, e.split
	cols, rows, mode, glyph := e.cols, e.rows, e.mode, e.glyph
	paused := e.paused
	oldTap, oldPane := e.tap, e.pane
	e.tap, e.pane = nil, nil
	e.tapGen = 0
	e.mu.Unlock()

	closeWebMedia(oldTap, oldPane)

	var lt *LevelTap
	var pane *videoPane
	if video {
		if pair.videoURL == "" {
			return
		}
		pane = newVideoPane(cols, rows, mode, glyph)
		if err := pane.startLive(pair.videoURL, 24, at, mode, glyph); err != nil {
			pane.Close()
			e.failStatus(gen, fmt.Errorf("video: %w", err))
			return
		}
	}
	if !video || split {
		if pair.tapURL == "" {
			if pane != nil {
				pane.Close()
			}
			return
		}
		var err error
		lt, err = StartLevelTapAt(pair.tapURL, at)
		if err != nil {
			if pane != nil {
				pane.Close()
			}
			e.failStatus(gen, fmt.Errorf("visualiser: %w", err))
			return
		}
	}
	e.mu.Lock()
	if e.gen != gen || e.closed {
		e.mu.Unlock()
		closeWebMedia(lt, pane)
		return
	}
	e.tap = lt
	e.pane = pane
	e.pos = at
	e.bumpLocked()
	e.mu.Unlock()
	if paused {
		if lt != nil {
			lt.SetPaused(true)
		}
		if pane != nil {
			pane.SetPaused(true)
		}
	}
}

func (e *webEngine) Report(pos, dur float64, ended bool) {
	if ended {
		e.Next()
		return
	}
	e.mu.Lock()
	if !e.hasTrack || e.loading {
		e.mu.Unlock()
		return
	}
	if dur > 0 {
		e.dur = dur
	}
	if pos >= 0 {
		e.pos = pos
	}
	e.bumpLocked()
	e.mu.Unlock()
}

func (e *webEngine) SetViewport(cols, rows int) {
	cols, rows = clampGrid(cols, rows)
	e.mu.Lock()
	if cols == e.cols && rows == e.rows {
		e.mu.Unlock()
		return
	}
	e.cols, e.rows = cols, rows
	e.viz.Resize(cols, rows)
	e.grid = NewVizGrid(cols, rows, e.mode != ColorNone, perCellFor(e.mode, e.glyph))
	e.grid.SetPalette(paletteAt(e.prefs.palette))
	needPane := e.pane != nil
	at := e.pos
	e.bumpLocked()
	e.mu.Unlock()
	if needPane {
		go e.restartPane(at)
	}
}

func (e *webEngine) SetViz(name string) error {
	if _, ok := VizByName(name); !ok {
		return fmt.Errorf("unknown visualizer %q", name)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, mk := range vizRegistry {
		if n := mk().Name(); n == name || strings.EqualFold(n, name) {
			e.prefs.style = i
			break
		}
	}
	v := e.prefs.makeViz()
	v.Resize(e.cols, e.rows)
	v.Reset()
	e.viz = v
	e.status = "viz " + e.prefs.StyleName()
	e.bumpLocked()
	return nil
}

func (e *webEngine) SetPalette(name string) error {
	idx := paletteIndexByName(name)
	if idx < 0 {
		return fmt.Errorf("unknown palette %q", name)
	}
	e.mu.Lock()
	e.prefs.palette = idx
	e.grid.SetPalette(paletteAt(idx))
	e.status = "palette " + name
	e.bumpLocked()
	e.mu.Unlock()
	return nil
}

func (e *webEngine) SetVideo(video bool) {
	e.mu.Lock()
	if e.video == video {
		e.mu.Unlock()
		return
	}
	e.video = video
	idx := e.index
	at := e.pos
	has := e.hasTrack
	e.mu.Unlock()
	if has {
		e.load(idx, at)
	}
}

func (e *webEngine) SetSplit(on bool) {
	e.mu.Lock()
	if e.split == on {
		e.mu.Unlock()
		return
	}
	e.split = on
	gen := e.gen
	video := e.video
	var closeTap *LevelTap
	var closePane *videoPane
	if !on {
		if video {
			closeTap = e.tap
			e.tap = nil
			e.tapGen = 0
		} else {
			closePane = e.pane
			e.pane = nil
		}
	}
	e.bumpLocked()
	e.mu.Unlock()
	if closeTap != nil {
		closeTap.Close()
	}
	if closePane != nil {
		closePane.Close()
	}
	if !on {
		return
	}
	if video {
		e.ensureTap(gen)
	} else {
		e.ensurePane(gen)
	}
}

// ensureTap mints the visualiser's own audio grant when the split view is turned
// on in video mode. It is deliberately lazy: the common path (video mode, split
// off) must not spend a second yt-dlp call on a spectrum nobody is looking at.
func (e *webEngine) ensureTap(gen uint64) {
	e.mu.Lock()
	if e.gen != gen || e.closed || !e.video || e.tap != nil || !e.hasTrack {
		e.mu.Unlock()
		return
	}
	track := e.cur.Track
	at := e.pos
	paused := e.paused
	e.mu.Unlock()

	var url string
	var err error
	if track.IsLocal() {
		url = track.LocalPath
	} else {
		url, _, _, err = audioURLOnce(track)
	}
	if err != nil {
		e.failStatus(gen, fmt.Errorf("split: %w", err))
		return
	}
	lt, err := StartLevelTapAt(url, at)
	if err != nil {
		e.failStatus(gen, fmt.Errorf("split: %w", err))
		return
	}
	e.mu.Lock()
	if e.gen != gen || e.closed || e.tap != nil {
		e.mu.Unlock()
		lt.Close()
		return
	}
	e.tap = lt
	e.bumpLocked()
	e.mu.Unlock()
	if paused {
		lt.SetPaused(true)
	}
}

// ensurePane resolves the video stream for a music-mode split pane. Music mode's
// pair deliberately has no video URL, so this is a third grant and is only spent
// when the user asks for a pane.
func (e *webEngine) ensurePane(gen uint64) {
	e.mu.Lock()
	if e.gen != gen || e.closed || e.video || e.pane != nil || !e.hasTrack {
		e.mu.Unlock()
		return
	}
	track := e.cur.Track
	paneURL := e.cur.paneURL
	cols, rows, mode, glyph := e.cols, e.rows, e.mode, e.glyph
	at := e.pos
	paused := e.paused
	e.mu.Unlock()

	if paneURL == "" {
		url, err := resolvePaneVideoURL(track, sourceHForCols(cols))
		if err != nil {
			e.failStatus(gen, fmt.Errorf("split: %w", err))
			return
		}
		paneURL = url
	}
	pane := newVideoPane(cols, rows, mode, glyph)
	if err := pane.startLive(paneURL, 24, at, mode, glyph); err != nil {
		pane.Close()
		e.failStatus(gen, fmt.Errorf("split: %w", err))
		return
	}
	e.mu.Lock()
	if e.gen != gen || e.closed || e.video || e.pane != nil {
		e.mu.Unlock()
		pane.Close()
		return
	}
	e.cur.paneURL = paneURL
	e.pane = pane
	e.bumpLocked()
	e.mu.Unlock()
	if paused {
		pane.SetPaused(true)
	}
}

// restartPane is the resize path for a live video pane. The pane's pixel width is
// ffmpeg's scale target, so a new width is a new decode; resizing the buffer
// alone leaves ffmpeg sending the old shape.
func (e *webEngine) restartPane(at float64) {
	e.mu.Lock()
	if e.pane == nil || e.loading || e.closed {
		e.mu.Unlock()
		return
	}
	gen := e.gen
	url := e.cur.pair.videoURL
	if url == "" {
		url = e.cur.paneURL
	}
	cols, rows, mode, glyph := e.cols, e.rows, e.mode, e.glyph
	paused := e.paused
	old := e.pane
	e.pane = nil
	e.mu.Unlock()
	old.Close()
	if url == "" {
		return
	}
	pane := newVideoPane(cols, rows, mode, glyph)
	if err := pane.startLive(url, 24, at, mode, glyph); err != nil {
		pane.Close()
		e.failStatus(gen, fmt.Errorf("video: %w", err))
		return
	}
	e.mu.Lock()
	if e.gen != gen || e.closed || e.pane != nil {
		e.mu.Unlock()
		pane.Close()
		return
	}
	e.pane = pane
	e.bumpLocked()
	e.mu.Unlock()
	if paused {
		pane.SetPaused(true)
	}
}

func (e *webEngine) MediaURL(token string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, ok := e.media[token]
	if !ok || m.gen != e.gen {
		return "", false
	}
	return m.url, true
}

// RefreshMedia re-mints the browser's audio grant after an upstream 403. A signed
// googlevideo URL is a cache with a lifetime, and a long pause can outlive it.
// One retry, because a second failure means the problem is not the grant.
func (e *webEngine) RefreshMedia(token string) (string, bool) {
	e.mu.Lock()
	m, ok := e.media[token]
	if !ok || m.gen != e.gen || !e.hasTrack {
		e.mu.Unlock()
		return "", false
	}
	gen := e.gen
	track := e.cur.Track
	e.mu.Unlock()
	if track.IsLocal() {
		return "", false
	}
	url, _, _, err := audioURLOnce(track)
	if err != nil {
		return "", false
	}
	e.mu.Lock()
	if e.gen != gen || e.media[token].gen != gen {
		e.mu.Unlock()
		return "", false
	}
	m.url = url
	e.media[token] = m
	e.mu.Unlock()
	return url, true
}

func trackKey(t Track) string {
	if t.ID != "" {
		return t.ID
	}
	if t.IsLocal() {
		return t.LocalPath
	}
	return t.Title + "|" + t.URL
}

func (e *webEngine) Download(index int) error {
	e.mu.Lock()
	if index < 0 || index >= len(e.queue) {
		e.mu.Unlock()
		return fmt.Errorf("no track at %d", index)
	}
	track := e.queue[index]
	e.mu.Unlock()
	return e.DownloadTrack(track)
}

// DownloadTrack saves one track, whether or not it is in the queue. The search
// view needs this: downloading a result should not silently append it to a queue
// the user was curating.
func (e *webEngine) DownloadTrack(track Track) error {
	if len(track.Title) > 500 {
		return fmt.Errorf("title too long")
	}
	if track.IsLocal() {
		return fmt.Errorf("%s is already a local file", track.Title)
	}
	if track.URL == "" {
		return fmt.Errorf("track has no url")
	}
	key := trackKey(track)
	e.mu.Lock()
	if st, ok := e.downloads[key]; ok && st != "download failed" && st != "downloaded" && st != "already downloaded" {
		e.mu.Unlock()
		return fmt.Errorf("already downloading")
	}
	e.downloads[key] = "starting"
	cacheDir := e.cacheDir
	e.bumpLocked()
	e.mu.Unlock()

	go func() {
		sink := &webDownloadSink{e: e, key: key}
		_, already, err := downloadTrack(track, cacheDir, sink)
		if err != nil {
			e.finishDownload(key, "download failed: "+err.Error())
			return
		}
		if already {
			e.finishDownload(key, "already downloaded")
		} else {
			e.finishDownload(key, "downloaded")
		}
		e.mu.Lock()
		serveFromCache(cacheDir, e.queue)
		e.bumpLocked()
		e.mu.Unlock()
	}()
	return nil
}

func palettePreviews() map[string][]string {
	out := make(map[string][]string, len(palettes))
	for _, p := range palettes {
		if p.mono {
			out[p.name] = []string{"#1b1b1b", "#4a4a4a", "#8a8a8a", "#e8e8e8"}
			continue
		}
		cs := make([]string, 0, 4)
		for _, t := range []float64{0.06, 0.34, 0.62, 0.94} {
			cs = append(cs, fmt.Sprintf("#%06x", p.fn(t, t, 0.35)))
		}
		out[p.name] = cs
	}
	return out
}

func (e *webEngine) setDownloadStatus(key, line string) {
	e.mu.Lock()
	e.downloads[key] = line
	e.bumpLocked()
	e.mu.Unlock()
}

func (e *webEngine) finishDownload(key, line string) {
	e.mu.Lock()
	e.downloads[key] = line
	e.bumpLocked()
	e.mu.Unlock()
}

func (e *webEngine) UpdateSettings(s settings) error {
	validated, err := s.validate()
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.cfg = validated
	if validated.CacheDir != "" {
		e.cacheDir = expandTilde(validated.CacheDir)
	}
	if validated.Viz != "" {
		for i, mk := range vizRegistry {
			if strings.EqualFold(mk().Name(), validated.Viz) {
				e.prefs.style = i
				break
			}
		}
	}
	if validated.Palette != "" {
		if i := paletteIndexByName(validated.Palette); i >= 0 {
			e.prefs.palette = i
		}
	}
	v := e.prefs.makeViz()
	v.Resize(e.cols, e.rows)
	v.Reset()
	e.viz = v
	e.grid.SetPalette(paletteAt(e.prefs.palette))
	wantVideo := e.video
	switch validated.Mode {
	case "video":
		wantVideo = true
	case "music":
		wantVideo = false
	}
	changedMode := wantVideo != e.video
	idx, at, has := e.index, e.pos, e.hasTrack
	e.bumpLocked()
	e.mu.Unlock()

	if changedMode && has {
		e.load(idx, at)
	}
	return saveSettings(validated)
}

func (e *webEngine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.gen++
	close(e.stop)
	t, p := e.tap, e.pane
	e.tap, e.pane = nil, nil
	clients := make([]*webClient, 0, len(e.clients))
	for c := range e.clients {
		clients = append(clients, c)
	}
	e.clients = map[*webClient]struct{}{}
	e.mu.Unlock()
	closeWebMedia(t, p)
	for _, c := range clients {
		c.close()
	}
	e.pumpWG.Wait()
}
