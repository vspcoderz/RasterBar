package main

import (
	"strings"
	"testing"
)

func TestParseYtdlpFlatPlaylist(t *testing.T) {
	raw := []byte(`{"_type":"url","id":"lTRiuFIWV54","url":"https://www.youtube.com/watch?v=lTRiuFIWV54","title":"1 A.M Study Session","channel":"Lofi Girl","uploader":"Lofi Girl","duration":3674,"thumbnails":[{"url":"small.jpg","height":202,"width":360},{"url":"big.jpg","height":720,"width":1280}]}
{"_type":"url","id":"abc123","url":"https://www.youtube.com/watch?v=abc123","title":"Second","channel":"Chan2","duration":0}`)
	tracks, err := parseYtdlp(raw)
	if err != nil {
		t.Fatalf("parseYtdlp: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(tracks))
	}
	got := tracks[0]
	if got.ID != "lTRiuFIWV54" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.DurationText() != "1:01:14" {
		t.Errorf("Duration = %q, want 1:01:14", got.DurationText())
	}
	if !strings.HasSuffix(got.Thumbs, "big.jpg") {
		t.Errorf("Thumbs = %q, want largest", got.Thumbs)
	}
	if got2 := tracks[1].DurationText(); got2 != "--:--" {
		t.Errorf("zero duration = %q, want --:--", got2)
	}
}

func TestParseYtdlpSkipsMalformed(t *testing.T) {
	tracks, err := parseYtdlp([]byte("not json\n{\"id\":\"ok\",\"title\":\"t\"}\n"))
	if err != nil {
		t.Fatalf("parseYtdlp: %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != "ok" {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
}

func TestParseYtdlpEmptyFails(t *testing.T) {
	if _, err := parseYtdlp([]byte("")); err == nil {
		t.Fatal("want error on empty input")
	}
}

func TestParseYtfzfJSONWithTerminalNoise(t *testing.T) {
	// Shape captured from a real `ytfzf -c yt -I J` run, wrapped in the escape
	// sequences fzf leaves behind (see PLAN.md).
	raw := []byte("\x1b[?1049h\x1bScraping Youtube (with https://www.youtube.com) (lofi)\n" +
		`[{"scraper":"youtube_search","url":"https://youtube.com/watch?v=BCxTQq0UiFs","title":"Chill Lofi","channel":"Art Is Sound","duration":"1:42:55","views":"6M","date":"1y ago","ID":"BCxTQq0UiFs","thumbs":"https://i.ytimg.com/x.jpg"}]` +
		"\x1b[?25h\x1b[?1049l\n")
	tracks, err := parseYtfzfJSON(raw)
	if err != nil {
		t.Fatalf("parseYtfzfJSON: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0].ID != "BCxTQq0UiFs" {
		t.Errorf("ID = %q", tracks[0].ID)
	}
	if tracks[0].ChannelText() != "Art Is Sound" {
		t.Errorf("channel = %q", tracks[0].ChannelText())
	}
	if tracks[0].DurationText() != "1:42:55" {
		t.Errorf("duration = %q", tracks[0].DurationText())
	}
}

func TestParseYtfzfJSONNoArrayFails(t *testing.T) {
	if _, err := parseYtfzfJSON([]byte("Nothing was scraped")); err == nil {
		t.Fatal("want error when no array present")
	}
}

func TestPickStreamsPrefersProgressive(t *testing.T) {
	formats := []ytFormat{
		{URL: "v720", Height: 720, VCodec: "av01", ACodec: "none", Protocol: "https", TBR: 100},
		{URL: "a251", ACodec: "opus", VCodec: "none", Protocol: "https", TBR: 140},
		{URL: "prog", Height: 360, VCodec: "avc1", ACodec: "mp4a", Protocol: "https"},
	}
	v, a, err := pickStreams(formats, 480)
	if err != nil {
		t.Fatalf("pickStreams: %v", err)
	}
	if v != "prog" || a != "prog" {
		t.Errorf("got (%q,%q), want the progressive stream used for both", v, a)
	}
}

func TestPickStreamsPicksSeparateWhenNoProgressive(t *testing.T) {
	formats := []ytFormat{
		{URL: "v1080-av1", Height: 1080, VCodec: "av01", ACodec: "none", Protocol: "https", TBR: 200},
		{URL: "v360-h264", Height: 360, VCodec: "avc1", ACodec: "none", Protocol: "https", TBR: 40},
		{URL: "v720-vp9", Height: 720, VCodec: "vp09", ACodec: "none", Protocol: "https", TBR: 90},
		{URL: "a140", ACodec: "mp4a", VCodec: "none", Protocol: "https", TBR: 129},
		{URL: "a139", ACodec: "mp4a", VCodec: "none", Protocol: "https", TBR: 49},
	}
	v, a, err := pickStreams(formats, 720)
	if err != nil {
		t.Fatalf("pickStreams: %v", err)
	}
	// h264 must win over vp9 and av1 at equal-or-better height, and the 1080p
	// AV1 stream must be excluded by the 720p cap.
	if v != "v360-h264" {
		t.Errorf("video = %q, want the h264 stream (cheapest to decode)", v)
	}
	if a != "a140" {
		t.Errorf("audio = %q, want the highest-bitrate audio", a)
	}
}

func TestPickStreamsHonoursHeightCap(t *testing.T) {
	formats := []ytFormat{
		{URL: "v1080", Height: 1080, VCodec: "avc1", ACodec: "none", Protocol: "https"},
		{URL: "v480", Height: 480, VCodec: "avc1", ACodec: "none", Protocol: "https"},
		{URL: "a", ACodec: "opus", VCodec: "none", Protocol: "https"},
	}
	v, _, err := pickStreams(formats, 480)
	if err != nil {
		t.Fatalf("pickStreams: %v", err)
	}
	if v != "v480" {
		t.Errorf("video = %q, want v480 (1080p exceeds the cap)", v)
	}
}

func TestPickStreamsSkipsM3u8(t *testing.T) {
	formats := []ytFormat{
		{URL: "v360-m3u8", Height: 360, VCodec: "avc1", ACodec: "none", Protocol: "m3u8"},
		{URL: "v240", Height: 240, VCodec: "avc1", ACodec: "none", Protocol: "https"},
		{URL: "a", ACodec: "opus", VCodec: "none", Protocol: "https"},
	}
	v, _, err := pickStreams(formats, 480)
	if err != nil {
		t.Fatalf("pickStreams: %v", err)
	}
	if v != "v240" {
		t.Errorf("video = %q, want v240; m3u8 must be skipped", v)
	}
}

func TestPickStreamsNoAudioFails(t *testing.T) {
	formats := []ytFormat{
		{URL: "v360", Height: 360, VCodec: "avc1", ACodec: "none", Protocol: "https"},
	}
	if _, _, err := pickStreams(formats, 480); err == nil {
		t.Error("want an error when there is no audio format")
	}
}

func TestIsH264(t *testing.T) {
	// "av01" is AV1, not h264, despite the shared prefix.
	for _, tc := range []struct {
		vc   string
		want bool
	}{
		{"avc1.640028", true},
		{"avc1.4d401e", true},
		{"av01.0.08M.08", false},
		{"vp09.00.40.08", false},
		{"vp8", false},
		{"", false},
	} {
		if got := isH264(tc.vc); got != tc.want {
			t.Errorf("isH264(%q) = %v, want %v", tc.vc, got, tc.want)
		}
	}
}

func TestLastURL(t *testing.T) {
	// Returns the LAST url, not the first: for a merged video+audio selector
	// yt-dlp can print separate DASH URLs, and taking the first gave us a
	// video-only stream with no audio.
	in := "warning line\nhttps://example.com/a\nhttps://example.com/b\n"
	if got := lastURL(in); got != "https://example.com/b" {
		t.Errorf("got %q, want the last url", got)
	}
	if got := lastURL("no urls here"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := lastURL("https://only.example.com/x\n"); got != "https://only.example.com/x" {
		t.Errorf("single url = %q", got)
	}
}

// --- HUD (S2) ---------------------------------------------------------------
//
// The clock delegates to formatDuration, which the browse list already uses, so
// a duration reads identically in the list and on the progress bar.
