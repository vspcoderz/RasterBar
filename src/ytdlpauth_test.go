package main

import "testing"

// TestYtdlpAuthArgs covers the precedence, which is the part someone will get
// wrong: a file was stated more precisely than a browser, so it wins, and the
// loser is not silently appended.
func TestYtdlpAuthArgs(t *testing.T) {
	cases := []struct {
		name string
		in   ytdlpAuthConfig
		want []string
	}{
		{"nothing", ytdlpAuthConfig{}, nil},
		{"browser", ytdlpAuthConfig{FromBrowser: "chromium"},
			[]string{"--cookies-from-browser", "chromium"}},
		{"file", ytdlpAuthConfig{FromFile: "/tmp/c.txt"},
			[]string{"--cookies", "/tmp/c.txt"}},
		{"file beats browser", ytdlpAuthConfig{FromFile: "/tmp/c.txt", FromBrowser: "brave"},
			[]string{"--cookies", "/tmp/c.txt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.args()
			if len(got) != len(c.want) {
				t.Fatalf("args() = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("args() = %v, want %v", got, c.want)
				}
			}
		})
	}
}

// TestYtdlpAuthEnvReadsBothVariables: the environment is what makes this usable at
// all, since a flagged machine needs it on every invocation.
func TestYtdlpAuthEnvReadsBothVariables(t *testing.T) {
	a := ytdlpAuthFromEnv([]string{
		"PATH=/usr/bin",
		"VSPZ_YT_CLI_COOKIES_FROM_BROWSER=chromium",
		"VSPZ_YT_CLI_DEBUG_SYNC=1",
	})
	if a.FromBrowser != "chromium" {
		t.Errorf("FromBrowser = %q, want chromium", a.FromBrowser)
	}
	if a.FromFile != "" {
		t.Errorf("FromFile = %q, want empty", a.FromFile)
	}
	if b := ytdlpAuthFromEnv([]string{"VSPZ_YT_CLI_COOKIES=/tmp/c.txt"}); b.FromFile != "/tmp/c.txt" {
		t.Errorf("FromFile = %q, want /tmp/c.txt", b.FromFile)
	}
	if c := ytdlpAuthFromEnv(nil); !c.empty() {
		t.Error("an empty environment produced an authentication setting")
	}
}

// TestExplainYtdlpErrorNamesTheFix is the point of the file. The raw message ends
// in two GitHub URLs and never says what to do about it; the only useful thing
// this program can do is say the one flag.
func TestExplainYtdlpErrorNamesTheFix(t *testing.T) {
	raw := "ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies-from-browser or --cookies for the authentication."

	got := explainYtdlpError(errExit1(), raw, ytdlpAuthConfig{}).Error()
	if !contains(got, "bot-checking") {
		t.Errorf("error does not name the problem:\n%s", got)
	}
	if !contains(got, "--cookies-from-browser") {
		t.Errorf("error does not name the fix:\n%s", got)
	}
	// Already authenticated: repeating "pass cookies" would be useless advice.
	again := explainYtdlpError(errExit1(), raw, ytdlpAuthConfig{FromBrowser: "brave"}).Error()
	if !contains(again, "another browser") && !contains(again, "wrong profile") {
		t.Errorf("second bot check still says to pass cookies:\n%s", again)
	}

	// Anything else keeps yt-dlp's own wording, which is more precise than
	// anything invented here.
	other := explainYtdlpError(errExit1(), "ERROR: Video unavailable. This video is private.", ytdlpAuthConfig{})
	if !contains(other.Error(), "private") {
		t.Errorf("a non-bot error lost yt-dlp's own reason:\n%s", other)
	}
	if contains(other.Error(), "cookies") {
		t.Errorf("cookies suggested for an unrelated failure:\n%s", other)
	}
}

type exitErr struct{}

func (exitErr) Error() string { return "exit status 1" }

func errExit1() error { return exitErr{} }

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && indexOf(hay, needle) >= 0
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
