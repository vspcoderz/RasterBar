package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Track is one search result. Field names match ytfzf's -I J output exactly
// (captured from a real PTY run — see PLAN.md).
type Track struct {
	Scraper  string  `json:"scraper"`
	URL      string  `json:"url"`
	Title    string  `json:"title"`
	Channel  string  `json:"channel"`
	Duration *string `json:"duration"`
	Views    *string `json:"views"`
	Date     *string `json:"date"`
	ID       string  `json:"ID"`
	Thumbs   string  `json:"thumbs"`

	// Series metadata, set only for library tracks. Zero values for anything
	// that came off the network, so every consumer can treat them as optional.
	Series    string `json:"series,omitempty"`
	Season    int    `json:"season,omitempty"`
	Episode   int    `json:"episode,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
}

// IsLocal reports whether this track is a file on disk rather than a remote
// stream.
//
// The distinction is not cosmetic, it decides which resolver runs. yt-dlp is
// the only way to get a playable URL for a youtube.com link (ffmpeg returns
// "Invalid data found when processing input" on one), but it is also a
// guaranteed failure on a local path, and running it just to be told that costs
// a subprocess on every seek. A file needs no resolving at all: ffmpeg and mpv
// both open a path directly.
func (t Track) IsLocal() bool {
	return t.LocalPath != ""
}

// YtdlpResult is one line of `yt-dlp --dump-json --flat-playlist` output.
type YtdlpResult struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	WebpageURL string `json:"webpage_url"`
	Title      string `json:"title"`
	Channel    string `json:"channel"`
	Uploader   string `json:"uploader"`
	Duration   int    `json:"duration"`
	Thumbnails []struct {
		URL    string `json:"url"`
		Height int    `json:"height"`
		Width  int    `json:"width"`
	} `json:"thumbnails"`
}

func (t Track) DurationText() string {
	if t.Duration == nil || *t.Duration == "" {
		return "--:--"
	}
	return *t.Duration
}

func (t Track) ChannelText() string {
	// A library file has no channel, and "unknown" in that column reads like a
	// scraping failure rather than like the truth. The series it belongs to is the
	// nearest equivalent, and it is the field a viewer would want there.
	if t.IsLocal() {
		if t.Series != "" {
			return t.Series
		}
		return "local"
	}
	if t.Channel == "" {
		return "unknown"
	}
	return t.Channel
}

// Search runs `ytfzf -c yt -I J <query>` under a PTY and parses its JSON.
//
// -c yt is mandatory: without it ytfzf queries a random Invidious instance and
// returns "Nothing was scraped" (verified 2026-10-01).
// -I J makes ytfzf print the results as JSON instead of playing them.
//
// ytfzf drives fzf interactively, so it needs a real terminal even when we
// only want data: without a PTY it fails with "inappropriate ioctl for
// device". runUnderPTY gives it one and auto-confirms the first result so fzf
// exits and the JSON lands on stdout.
func Search(query string) ([]Track, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	out, err := runUnderPTY("ytfzf", []string{"-c", "yt", "-I", "J", query})
	if err != nil {
		return nil, fmt.Errorf("ytfzf: %w", err)
	}
	tracks, yerr := parseYtfzfJSON(out)
	if yerr == nil && len(tracks) > 0 {
		return tracks, nil
	}

	// Fall back to yt-dlp. ytfzf's scrape depends on a reachable Invidious
	// path and its fzf UI is not always scriptable; yt-dlp always works.
	tracks, derr := searchYtDlp(query)
	if derr != nil {
		if yerr != nil {
			return nil, fmt.Errorf("ytfzf: %v; yt-dlp: %w", yerr, derr)
		}
		return nil, derr
	}
	return tracks, nil
}

func searchYtDlp(query string) ([]Track, error) {
	cmd := exec.Command("yt-dlp",
		fmt.Sprintf("ytsearch10:%s", query),
		"--dump-json", "--flat-playlist", "--no-warnings")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp search: %w: %s", err, stderr.String())
	}
	return parseYtdlp(out)
}

// parseYtfzfJSON extracts the JSON array from ytfzf's -I J output. The stream
// contains fzf/terminal escape sequences around it, so we locate the first '['
// and decode from there, ignoring whatever trails.
func parseYtfzfJSON(out []byte) ([]Track, error) {
	s := string(out)
	// Find a '[' that actually begins a JSON array. A naive IndexByte finds the
	// '[' inside fzf's own escape sequences (\x1b[?25h) and decoding starts
	// mid-escape: "invalid character '?'". Require '[' plus optional
	// whitespace plus '{' (or ']' for an empty array).
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] != '[' {
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\r' || s[j] == '\t') {
			j++
		}
		if j < len(s) && (s[j] == '{' || s[j] == ']') {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("no JSON array in ytfzf output")
	}
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	var tracks []Track
	if err := dec.Decode(&tracks); err != nil {
		return nil, fmt.Errorf("decode ytfzf json: %w", err)
	}
	// ytfzf emits youtube.com/watch?v=ID links and an ID field; make sure the
	// URL is usable downstream.
	for i := range tracks {
		if tracks[i].ID != "" && tracks[i].URL == "" {
			tracks[i].URL = "https://youtube.com/watch?v=" + tracks[i].ID
		}
	}
	return tracks, nil
}

func parseYtdlp(out []byte) ([]Track, error) {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	tracks := make([]Track, 0, len(lines))
	for _, l := range lines {
		l = bytes.TrimSpace(l)
		if len(l) == 0 {
			continue
		}
		var y YtdlpResult
		if err := json.Unmarshal(l, &y); err != nil {
			continue
		}
		if y.ID == "" {
			continue
		}
		tr := Track{
			Scraper: "youtube_search",
			ID:      y.ID,
			Title:   y.Title,
			Channel: coalesce(y.Channel, y.Uploader),
			URL:     coalesce(y.WebpageURL, y.URL),
		}
		if y.Duration > 0 {
			d := formatDuration(y.Duration)
			tr.Duration = &d
		}
		if len(y.Thumbnails) > 0 {
			best := y.Thumbnails[0]
			for _, th := range y.Thumbnails {
				if th.Height > best.Height {
					best = th
				}
			}
			tr.Thumbs = best.URL
		}
		tracks = append(tracks, tr)
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("no results from yt-dlp")
	}
	return tracks, nil
}

func coalesce(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func formatDuration(s int) string {
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}
