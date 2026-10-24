package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// save_test.go covers the background save started by playback: the fragment
// reassembly in saveJob, the outcome states the HUD reads, and the registry's
// one-job-per-video-ID rule. All of it runs against a fake yt-dlp, like
// ytdlpauth_test.go — a real download is the manual tier.

// TestSaveJobWriteReassemblesLines is the interesting half of the sink: the
// exec copier may hand over a chunk that ends mid-line, and those bytes belong
// to the NEXT line. Parsing the fragment as a line would show a false "42.3%"
// and then never show the real "43.1%" that replaces it.
func TestSaveJobWriteReassemblesLines(t *testing.T) {
	j := &saveJob{}
	j.Write([]byte("[download]  1.2% of 8"))
	if got := j.line(); got != "" {
		t.Errorf("an open fragment leaked as a line: %q", got)
	}
	j.Write([]byte(".0MiB at 1.0MiB/s\n"))
	want := "saving — 1.2% of 8.0MiB at 1.0MiB/s"
	if got := j.line(); got != want {
		t.Errorf("reassembly = %q, want %q", got, want)
	}
}

// TestSaveJobTakeTrimsYtdlpTags: the HUD row already names the track, so
// "[download] " and "[Merger] " prefixes are log noise nobody wants to read.
// Hand-worked cases straight from a real yt-dlp transcript.
func TestSaveJobTakeTrimsYtdlpTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[download]   0.5% of  218.53KiB at  232.71KiB/s ETA 00:00", "0.5% of  218.53KiB at  232.71KiB/s ETA 00:00"},
		{"[Merger] Merging formats into \"x.mp4\"", "Merging formats into \"x.mp4\""},
		{"[youtube] dQw: Downloading webpage", "dQw: Downloading webpage"},
		{"  42.3% of 44.5MiB", "42.3% of 44.5MiB"},
		{"   ", ""},        // whitespace only: no fragment at all
		{"[download]", ""}, // tag with no content: skipped, not leaked
	}
	for _, c := range cases {
		j := &saveJob{}
		j.take(c.in)
		got := j.line()
		if c.want == "" {
			if got != "" {
				t.Errorf("take(%q) surfaced %q, want nothing", c.in, got)
			}
			continue
		}
		want := "saving — " + c.want
		if len(want) > len("saving — ")+56 {
			t.Fatalf("test line longer than the display cap, unsupported case: %q", c.in)
		}
		if got != want {
			t.Errorf("take(%q) = %q, want %q", c.in, got, want)
		}
	}

	// A line longer than the display cap is cut, not shown whole: an ETA slug
	// carries the interesting part, and the HUD row would clip the tail anyway.
	j := &saveJob{}
	long := "[download] " + strings.Repeat("a", 80)
	j.take(long)
	if got := j.line(); len(got) != len("saving — ")+56 {
		t.Errorf("long line not capped: %d chars", len(got)-len("saving — "))
	}
}

// TestSaveJobOutcomeStates covers what line() says when the download resolves:
// success names the cached file, failure names the reason, and neither is
// prefixed "saving" — a done download that calls itself "saving" looks stuck.
func TestSaveJobOutcomeStates(t *testing.T) {
	j := &saveJob{}
	j.done = true
	j.last = "River [ff7id].mp4"
	if got := j.line(); got != "cached — River [ff7id].mp4" {
		t.Errorf("success line = %q", got)
	}
	j.failed = true
	if got := j.line(); got != "save failed — River [ff7id].mp4" {
		t.Errorf("failure line = %q", got)
	}
}

// TestStartSaveDedupesByID: two starts for one video ID return one job —
// two yt-dlp processes on the same ID would fight over one .part file. The
// registry insert is synchronous, so the second call needs no settle time.
func TestStartSaveDedupesByID(t *testing.T) {
	dir := t.TempDir()
	cleanup := ytdlpFake(t, `
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then
    d=$(dirname "$a")
    mkdir -p "$d"
    echo "" > "$d/Dupe [dupeid1].mp4"
  fi
  prev="$a"
done
sleep 5`)
	defer cleanup()

	tr := Track{ID: "dupeid1", URL: "https://dupe.example/a"}
	j1 := startSave(tr, dir)
	if j1 == nil {
		t.Fatal("a remote track with a cache dir produced no job")
	}
	j2 := startSave(tr, dir)
	if j1 != j2 {
		t.Error("the second startSave started a second job for one ID")
	}
	if j1.id != "dupeid1" {
		t.Errorf("job id = %q, want dupeid1", j1.id)
	}
}

// TestStartSaveNilWhenNothingToSave: the local/no-URL/empty-dir cases produce
// no job and, importantly, no goroutine — a nil *saveJob is the session's
// "no HUD reporting" signal, mirroring the presence convention.
func TestStartSaveNilWhenNothingToSave(t *testing.T) {
	if startSave(Track{LocalPath: "/tmp/a.mp4", URL: "https://x.example"}, "/tmp") != nil {
		t.Error("a local track produced a save job")
	}
	if startSave(Track{}, "/tmp") != nil {
		t.Error("a track with no URL produced a save job")
	}
	if startSave(Track{ID: "x", URL: "https://x.example"}, "") != nil {
		t.Error("an empty cache dir produced a save job")
	}
}

// TestStartSaveFullRunDownloadsAndFinishes is the whole path at once: the fake
// yt-dlp prints two progress lines (which the job must have seen by the time it
// resolves) and writes the file it was -o'd to name. What the HUD must never
// say is "saving" after the run has ended.
func TestStartSaveFullRunDownloadsAndFinishes(t *testing.T) {
	dir := t.TempDir()
	cleanup := ytdlpFake(t, `
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then
    d=$(dirname "$a")
    mkdir -p "$d"
    echo "" > "$d/Full [fullid9].mp4"
  fi
  prev="$a"
done
echo "[download]  1.2% of 8.0MiB at 1.0MiB/s"
echo "[download] 100% of 8.0MiB"`)
	defer cleanup()

	tr := Track{ID: "fullid9", URL: "https://full.example/x"}
	j := startSave(tr, dir)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m := j.line(); strings.HasPrefix(m, "cached — ") {
			if m != "cached — Full [fullid9].mp4" {
				t.Errorf("final line = %q", m)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never resolved; last state %q", j.line())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The file really is findable afterwards, in exactly the form the next
	// play's cacheLookup will read — otherwise "cached —" is a claim the
	// cache cannot keep.
	if p, ok := cacheLookup(dir, "fullid9"); !ok || filepath.Base(p) != "Full [fullid9].mp4" {
		t.Errorf("cacheLookup after run = %q, %v", p, ok)
	}
}

// TestStartSaveReplacesFailed: one play attempted it and lost; the next play
// gets a fresh attempt rather than being handed a dead job that reports only
// the old failure.
func TestStartSaveReplacesFailed(t *testing.T) {
	savesMu.Lock()
	saves = map[string]*saveJob{}
	savesMu.Unlock()
	defer func() {
		savesMu.Lock()
		saves = map[string]*saveJob{}
		savesMu.Unlock()
	}()

	dir := t.TempDir()
	cleanup := ytdlpFake(t, `echo "ERROR: Video unavailable" >&2; exit 1`)
	defer cleanup()

	tr := Track{ID: "failid7", URL: "https://fail.example/x"}
	j1 := startSave(tr, dir)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.HasPrefix(j1.line(), "save failed — ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first job never failed: %q", j1.line())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The fake now succeeds, so the replacement's success tells the two apart.
	cleanup2 := ytdlpFake(t, `
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then
    d=$(dirname "$a"); mkdir -p "$d"; echo "" > "$d/Retry [failid7].mp4"
  fi
  prev="$a"
done`)
	defer cleanUp2(cleanup2)

	j2 := startSave(tr, dir)
	if j1 == j2 {
		t.Error("a failed job was returned instead of replaced")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if strings.HasPrefix(j2.line(), "cached — ") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("replacement job never resolved: %q", j2.line())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func cleanUp2(c func()) { c() }

// TestRunDownloadStreamsStdout pins the stdout carrying the progress: a fake
// yt-dlp that logs lines and exits 0 must be visible in the sink WHILE ALSO
// returning through the captured buffer. This is the regression test for the
// "progress went to stdout, we captured stdout, silence" bug — a streaming
// test on stderr passes forever while the real stream dies.
func TestRunDownloadStreamsStdout(t *testing.T) {
	cleanup := ytdlpFake(t, `
echo "[download]  10.0% of 10MiB"
echo "[download]  50.0% of 10MiB"
echo "final"`)
	defer cleanup()

	var a ytdlpAuthConfig
	var sink bytes.Buffer
	out, err := a.runDownload([]string{"-v", "https://x.example"}, &sink)
	if err != nil {
		t.Fatalf("runDownload: %v", err)
	}
	for _, line := range []string{"[download]  10.0% of 10MiB", "[download]  50.0% of 10MiB", "final"} {
		if !strings.Contains(sink.String(), line) {
			t.Errorf("sink missed %q (got %q)", line, sink.String())
		}
		if !strings.Contains(string(out), line) {
			t.Errorf("captured stdout missed %q (got %q)", line, out)
		}
	}
	// A nil sink degenerates to run(): the download path with nobody watching.
	if _, err := a.runDownload([]string{"-v"}, nil); err != nil {
		t.Errorf("nil sink failed: %v", err)
	}
}

// TestRunDownloadRetryReusesTheSink: a fake that crashes once then succeeds
// must stream BOTH attempts, and the crash's exit must not lose the sink or
// the captured bytes of the surviving attempt.
func TestRunDownloadRetryReusesTheSink(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "n")
	cleanup := ytdlpFake(t, `
if [ ! -f `+counter+` ]; then
  touch `+counter+`
  kill -SEGV $$
fi
echo "[download] 2.0% of retry"`)
	defer cleanup()

	var a ytdlpAuthConfig
	var sink bytes.Buffer
	out, err := a.runDownload([]string{"-J"}, &sink)
	if err != nil {
		t.Fatalf("retry download failed: %v", err)
	}
	if n := strings.Count(sink.String(), "[download] 2.0% of retry"); n != 1 {
		t.Errorf("surviving attempt streamed %d times, want 1: %q", n, sink.String())
	}
	if !strings.Contains(string(out), "[download] 2.0% of retry") {
		t.Errorf("captured stdout lost the retry's lines: %q", out)
	}
}

// TestDownloadTrackStreamsToSink walks downloadTrack itself over the fake
// above's shape, because the verb and the saver go through it and not through
// runDownload directly.
func TestDownloadTrackStreamsToSink(t *testing.T) {
	dir := t.TempDir()
	cleanup := ytdlpFake(t, `
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then
    d=$(dirname "$a"); mkdir -p "$d"; echo "" > "$d/Sunk [sinkid12].mp4"
  fi
  prev="$a"
done
echo "[download]  7.0% of 3MiB"`)
	defer cleanup()

	var sink bytes.Buffer
	tr := Track{ID: "sinkid12", URL: "https://sunk.example/x"}
	path, already, err := downloadTrack(tr, dir, &sink)
	if err != nil || already {
		t.Fatalf("downloadTrack = %q, %v, %v", path, already, err)
	}
	if !strings.Contains(sink.String(), "[download]  7.0% of 3MiB") {
		t.Errorf("sink missed the progress line: %q", sink.String())
	}
}
