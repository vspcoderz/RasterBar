package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Web frame protocol.
//
// The browser is not a terminal, so the diff renderers are deliberately not in
// this path: their output is cursor moves and SGR elision, which a <canvas> and
// a <pre> cannot consume without a terminal emulator. What IS shared is the
// thing that matters, the frame the renderers are fed: RGB24 pixels for colour
// and one byte per cell for mono. The web layer forwards that buffer unchanged.
//
// A packet is [type:1][length:4 big-endian][payload]. The same stream carries
// JSON state changes and frames, so one fetch() connection drives the whole UI
// and there is no WebSocket upgrade to hand-roll.

const (
	webPktState byte = 1
	webPktFrame byte = 2
)

const (
	webPaneViz   byte = 0
	webPaneVideo byte = 1
)

// A frame payload starts with a 7-byte header:
//
//	[source:1][mono:1][cols:2][rows:2][pixPerCell:1]
//
// mono is 1 for one byte per cell. In mono the meaning of that byte differs by
// source: the visualiser grid already holds ramp indices, while the video tap
// emits 0..255 luminance. Keeping the source byte next to it is what lets the
// client pick the right mapping instead of the server pre-converting one path
// and not the other.

func webPacket(typ byte, payload []byte) []byte {
	p := make([]byte, 5+len(payload))
	p[0] = typ
	binary.BigEndian.PutUint32(p[1:5], uint32(len(payload)))
	copy(p[5:], payload)
	return p
}

// webFramePacket encodes one frame. data must already be in the layout the
// header describes; this copies it into the packet so the producer can reuse
// its scratch buffer immediately.
func webFramePacket(source byte, mode ColorMode, glyph GlyphMode, cols, rows int, data []byte) []byte {
	payload := make([]byte, 7+len(data))
	payload[0] = source
	if mode == ColorNone {
		payload[1] = 1
	}
	binary.BigEndian.PutUint16(payload[2:4], uint16(cols))
	binary.BigEndian.PutUint16(payload[4:6], uint16(rows))
	payload[6] = byte(perCellFor(mode, glyph))
	copy(payload[7:], data)
	return webPacket(webPktFrame, payload)
}

func webStatePacket(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return webPacket(webPktState, b), nil
}

// webClient is one open /api/events connection.
//
// Frames are dropped, never queued. A tab that stops reading (backgrounded,
// throttled, or on a 5GHz-interrupted link) must not make the analysis pump
// wait: there is always a newer frame, so the only correct frame to send is the
// latest one. State is not sent this way; it is read from the engine by the
// handler, because losing a state change is not self-healing the way a frame is.
type webClient struct {
	frames chan []byte
	done   chan struct{}
	once   sync.Once
}

func newWebClient() *webClient {
	return &webClient{
		frames: make(chan []byte, 1),
		done:   make(chan struct{}),
	}
}

func (c *webClient) pushFrame(p []byte) {
	select {
	case c.frames <- p:
		return
	default:
	}
	// Full: throw the stale frame away and try once more. A non-blocking
	// receive is safe here because this goroutine is the only sender.
	select {
	case <-c.frames:
	default:
	}
	select {
	case c.frames <- p:
	default:
	}
}

func (c *webClient) close() {
	c.once.Do(func() { close(c.done) })
}

// webDownloadSink turns yt-dlp's stdout into a one-line status for the UI.
//
// yt-dlp writes [download] progress as whole lines because runDownload passes
// --newline; the terminal's rule about a split line still applies here, so the
// fragment after the last newline is held for the next write instead of being
// shown as a stale percentage.
type webDownloadSink struct {
	e   *webEngine
	key string

	mu       sync.Mutex
	tail     []byte
	last     time.Time
	lastLine string
}

func (s *webDownloadSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.tail = append(s.tail, p...)
	for {
		i := indexByte(s.tail, '\n')
		if i < 0 {
			break
		}
		raw := s.tail[:i]
		s.tail = s.tail[i+1:]
		if line := cleanDownloadLine(string(raw)); line != "" {
			s.lastLine = line
		}
	}
	line := s.lastLine
	s.lastLine = ""
	now := time.Now()
	send := line != "" && now.Sub(s.last) >= 200*time.Millisecond
	if send {
		s.last = now
	}
	s.mu.Unlock()
	if send {
		s.e.setDownloadStatus(s.key, line)
	}
	return len(p), nil
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func cleanDownloadLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && (s[0] == '\r' || s[0] == ' ') {
		s = s[1:]
	}
	return s
}

// webState is the JSON half of the protocol. Everything the UI renders that is
// not a frame comes from here, so a reload can redraw the whole app from one
// object.
type webState struct {
	Version  string   `json:"version"`
	Ramp     string   `json:"ramp"`
	Styles   []string `json:"styles"`
	Palettes []string `json:"palettes"`

	Queue    []webQueueItem `json:"queue"`
	Index    int            `json:"index"`
	Has      bool           `json:"hasTrack"`
	Title    string         `json:"title"`
	Channel  string         `json:"channel"`
	Dur      float64        `json:"duration"`
	Pos      float64        `json:"position"`
	Thumb    string         `json:"thumb,omitempty"`
	TrackID  string         `json:"trackId,omitempty"`
	TrackURL string         `json:"trackUrl,omitempty"`
	Local    bool           `json:"local"`

	Paused  bool   `json:"paused"`
	Volume  int    `json:"volume"`
	Music   bool   `json:"music"`
	Video   bool   `json:"video"`
	Split   bool   `json:"split"`
	Loading bool   `json:"loading"`
	Status  string `json:"status"`
	Error   string `json:"error"`

	Cols int `json:"cols"`
	Rows int `json:"rows"`

	Viz           string              `json:"viz"`
	Palette       string              `json:"palette"`
	PaletteColors map[string][]string `json:"paletteColors"`
	Audio         string              `json:"audioSrc"`

	CacheDir    string            `json:"cacheDir"`
	Settings    settings          `json:"settings"`
	Downloading map[string]string `json:"downloading,omitempty"`
}

type webQueueItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Channel  string `json:"channel"`
	Duration string `json:"duration"`
	Thumb    string `json:"thumb,omitempty"`
	Local     bool   `json:"local"`
	LocalPath string `json:"local_path,omitempty"`
	URL       string `json:"url,omitempty"`
	Series   string `json:"series,omitempty"`
}

func queueItem(t Track) webQueueItem {
	return webQueueItem{
		ID:        t.ID,
		Title:     t.Title,
		Channel:   t.ChannelText(),
		Duration:  t.DurationText(),
		Thumb:     t.Thumbs,
		Local:     t.IsLocal(),
		LocalPath: t.LocalPath,
		URL:       t.URL,
		Series:    t.Series,
	}
}

func (s webState) string() string {
	return fmt.Sprintf("%s (%d/%d)", s.Title, s.Index+1, len(s.Queue))
}
