package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ytdlpauth.go teaches yt-dlp who you are.
//
// YouTube bot-checks by IP, and when it decides a machine is a scraper every
// request comes back "Sign in to confirm you're not a bot" — for searches and for
// playback alike. It is not a bug here and no amount of retrying fixes it; the
// only cure is to hand yt-dlp the cookies of a browser that is already signed in.
//
// Which is a thing this program could not do. It built every yt-dlp invocation
// from a fixed argument list, so there was no way to pass `--cookies` or
// `--cookies-from-browser` even when you knew that was the answer. The error came
// back verbatim, ending in two URLs, and the actual instruction — one flag — was
// nowhere in it.
//
// Everything routes through one builder so a setting cannot be applied to the
// search and forgotten on the playback path, or vice versa.

// ytdlpAuth is the authentication every yt-dlp call carries. A package-level
// value rather than a parameter threaded through three call sites and a dozen
// callers, because threading it would mean every future yt-dlp invocation has to
// remember — and the failure is invisible on the paths that did remember.
var ytdlpAuth ytdlpAuthConfig

// ytdlpAuthConfig is the extra argument list every yt-dlp call carries.
//
// Empty means "add nothing", which is the correct default for a machine YouTube
// has not flagged and keeps the common case free of a stray argument.
type ytdlpAuthConfig struct {
	// FromBrowser names a browser profile for --cookies-from-browser.
	FromBrowser string
	// FromFile is a Netscape cookies.txt for --cookies.
	FromFile string
}

// ytdlpAuthFromEnv reads the setting from the environment, so a machine that needs
// it does not need the flag on every invocation.
//
// VSPZ_YT_CLI_* rather than a config file: this program has one, deliberately —
// zero dependencies and nothing to parse — and a config file for a single string
// would be a worse trade than an environment variable. Same prefix as the debug
// variables already here.
func ytdlpAuthFromEnv(env []string) ytdlpAuthConfig {
	var a ytdlpAuthConfig
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "VSPZ_YT_CLI_COOKIES_FROM_BROWSER="):
			a.FromBrowser = strings.TrimPrefix(kv, "VSPZ_YT_CLI_COOKIES_FROM_BROWSER=")
		case strings.HasPrefix(kv, "VSPZ_YT_CLI_COOKIES="):
			a.FromFile = strings.TrimPrefix(kv, "VSPZ_YT_CLI_COOKIES=")
		}
	}
	return a
}

// args returns the authentication flags, or nothing.
//
// Order is fixed and the two are mutually exclusive in practice: a file wins,
// because it is the one that was stated more precisely and the browser flag is
// the fallback. Saying so beats silently preferring one.
func (a ytdlpAuthConfig) args() []string {
	switch {
	case a.FromFile != "":
		return []string{"--cookies", a.FromFile}
	case a.FromBrowser != "":
		return []string{"--cookies-from-browser", a.FromBrowser}
	}
	return nil
}

// empty reports whether there is nothing to add.
func (a ytdlpAuthConfig) empty() bool { return len(a.args()) == 0 }

// ytdlpAuthFrom builds the full argument list for a yt-dlp invocation.
//
// Every call site goes through here rather than appending to a literal, which is
// the two-copies-of-one-rule hazard: there are three yt-dlp invocations (search,
// playback, metadata) and a setting applied to one of them is a bug report that
// depends on which code path the user happened to be on.
func (a ytdlpAuthConfig) withArgs(base ...string) []string {
	return append(append([]string{}, base...), a.args()...)
}

// run executes yt-dlp and returns its stdout.
//
// stderr is captured rather than inherited, for two reasons. The subprocess runs
// under raw mode with the cursor hidden and the screen owned by the renderer, so
// a progress line printed straight to the terminal lands in the middle of the
// picture. And the error text is the only way to recognise the bot check, which
// needs to be turned into an instruction instead of being passed through.
func (a ytdlpAuthConfig) run(args ...string) ([]byte, error) {
	cmd := exec.Command("yt-dlp", a.withArgs(args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, explainYtdlpError(err, stderr.String(), a)
	}
	return out, nil
}

// botCheck is what YouTube says when it wants a logged-in browser.
const botCheck = "Sign in to confirm"

// explainYtdlpError turns yt-dlp's failure into something actionable.
//
// The bot check gets a real answer, because it is the one failure whose remedy
// is a single flag and whose raw text does not mention it. Everything else keeps
// yt-dlp's own message, which is more precise than anything invented here.
func explainYtdlpError(err error, stderr string, a ytdlpAuthConfig) error {
	detail := strings.TrimSpace(stderr)
	if !strings.Contains(detail, botCheck) {
		if detail == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, firstLines(detail, 3))
	}

	// Already authenticated and still blocked: the cookies are stale, the wrong
	// profile was named, or the account is flagged. Saying "pass cookies" again
	// would be useless advice, so name the next thing instead.
	switch {
	case !a.empty():
		return fmt.Errorf("YouTube is still bot-checking you even with %s.\n"+
			"  The cookies are probably signed out or the wrong profile was named.\n"+
			"  Try another browser, or export a cookies.txt and use --cookies.\n"+
			"  %s", strings.Join(a.args(), " "), firstLines(detail, 2))
	case hasBrowserCookieDirs():
		return fmt.Errorf("YouTube is bot-checking this machine, so yt-dlp needs cookies.\n"+
			"  Found a browser that has some: rasterbar --cookies-from-browser <name>\n"+
			"  Or set it once:  export VSPZ_YT_CLI_COOKIES_FROM_BROWSER=<name>\n"+
			"  Or pass a cookies.txt: rasterbar --cookies FILE\n"+
			"  %s", firstLines(detail, 2))
	default:
		return fmt.Errorf("YouTube is bot-checking this machine, so yt-dlp needs cookies.\n"+
			"  rasterbar --cookies-from-browser firefox|chrome|brave|chromium\n"+
			"  or --cookies FILE, or set VSPZ_YT_CLI_COOKIES_FROM_BROWSER=<name>\n"+
			"  %s", firstLines(detail, 2))
	}
}

// hasBrowserCookieDirs reports whether this machine has a browser yt-dlp could
// read cookies from, so the error can suggest a name rather than a shrug.
func hasBrowserCookieDirs() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, d := range []string{
		".mozilla/firefox",
		".config/BraveSoftware/Brave-Browser",
		".config/google-chrome",
		".config/chromium",
	} {
		if _, err := os.Stat(home + "/" + d); err == nil {
			return true
		}
	}
	return false
}

// firstLines trims a multi-line subprocess message to something that fits on a
// terminal without losing the part that matters.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	var kept []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "WARNING:") {
			continue
		}
		kept = append(kept, l)
		if len(kept) == n {
			break
		}
	}
	return strings.Join(kept, " ")
}
