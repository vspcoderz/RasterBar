package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// update.go is self-update, built only from the stdlib.
//
// The rules here come from the same lesson the build pipeline learned the hard
// way on this machine (TASTE: a build that writes over live output can leave a
// half-written instance behind): never overwrite the running artifact in one
// step. The new binary is downloaded to a temp file *in the binary's own
// directory* — rename only works in one filesystem, and os.Rename across one
// fails or copies — verified to be a real ELF, chmod'd, exchanged with a
// rename. The old instance keeps running from the old inode until it exits,
// which Linux guarantees even after the replace.
//
// The release source is GitHub's REST API. One endpoint (`releases/latest`),
// no auth, no pagination: enough for a nightly check of this one repository.

// updateRepo is the GitHub repository with the releases.
const updateRepo = "vspcoderz/rasterbar"

// updateTimeout bounds the releases API call: an update check must fail fast
// and loudly near the program it serves rather than hang the shell. The asset
// download gets its own budget (updateDownloadTimeout), because a multi-MB
// binary over a slow connection was killed by a 10s whole-body deadline —
// verified failing with "context deadline exceeded" on real hardware.
const updateTimeout = 10 * time.Second

const updateDownloadTimeout = 5 * time.Minute

// ghRelease is the small slice of GitHub's release JSON that matters.
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// updateClient never retries. A failed release check is a network fact, not a
// transient: retrying turns "no internet" into a wait instead of a verdict.
func updateClient() *http.Client {
	return &http.Client{Timeout: updateTimeout}
}

// fetchLatestRelease reads the current release. The primary error text is kept
// and surfaced: "no route to host" on a first-time setup is not the same bug as
// a 404, and the user can only act on knowing which.
func fetchLatestRelease() (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", updateRepo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub requires a User-Agent; a bare default gets a 403 and teaches
	// nothing. Naming the program (and its version, when known) is honest.
	ua := "rasterbar"
	if version != "" {
		ua += "/" + version
	}
	req.Header.Set("User-Agent", ua)
	resp, err := updateClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch update info: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("update check: %s returned %s", updateRepo, resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("update check: decode release: %w", err)
	}
	resp.Body.Close()
	if rel.TagName == "" {
		return nil, fmt.Errorf("update check: %s has no tagged release", updateRepo)
	}
	return &rel, nil
}

// pickUpdateAsset selects the release asset for this platform, locating the
// file by its name because a GitHub release has no manifest. Requiring both
// the OS and the architecture keeps a linux/amd64 binary from standing in for
// a linux/arm64 one; the error lists what WAS there, because "no asset" is a
// claim about this particular release, and the reader can see that claim.
func pickUpdateAsset(rel *ghRelease, osName, arch string) (*ghAsset, error) {
	for i := range rel.Assets {
		n := strings.ToLower(rel.Assets[i].Name)
		if strings.Contains(n, osName) && strings.Contains(n, arch) {
			return &rel.Assets[i], nil
		}
	}
	names := make([]string, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		names = append(names, a.Name)
	}
	return nil, fmt.Errorf(
		"%s %s has no asset for this build (looking for %s/%s); present: %s",
		updateRepo, rel.TagName, osName, arch, strings.Join(names, ", "))
}

// parseVersion reduces "v1.2.3" or "v1.2.3-rc1" into comparable integers.
// Anything unparsable ends the parse; versions that cannot be compared are
// treated as equal (never newer), which is the conservative answer.
func parseVersion(s string) []int {
	s = strings.TrimPrefix(s, "v")
	fields := strings.Split(s, ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		// Trailing non-numeric suffixes ("3-1", "3-rc1") keep the field's
		// leading integer: a Debian-style package revision does not change
		// what version of the program this is.
		for i := 0; i < len(f); i++ {
			if f[i] < '0' || f[i] > '9' {
				f = f[:i]
				break
			}
		}
		if f == "" {
			break
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// newerVersion reports whether one tag is strictly greater than another.
// Missing components compare as zero, so v1.9 -> v1.9.1 counts as newer,
// and equal versions count as up to date.
func newerVersion(current, latest string) bool {
	c, l := parseVersion(current), parseVersion(latest)
	if len(c) == 0 || len(l) == 0 {
		return false
	}
	for i := 0; i < len(l) || i < len(c); i++ {
		li, ci := 0, 0 // a missing component compares as zero
		if i < len(l) {
			li = l[i]
		}
		if i < len(c) {
			ci = c[i]
		}
		if li != ci {
			return li > ci
		}
	}
	return false
}

// elfMagic means the bytes are a loadable binary, not the rate-limited error
// page or the HTML login redirect a download URL sometimes serves. No check
// below it costs more than a few MB of disk.
var elfMagic = []byte{0x7f, 'E', 'L', 'F'}

// installAsset downloads one release asset into the running binary's
// directory and exchanges it for the running exe.
func installAsset(a *ghAsset) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate running binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", fmt.Errorf("resolve binary path: %w", err)
	}
	// Temp file must be a sibling of the target: same filesystem is what makes
	// the final rename atomic instead of a copy that could be interrupted with
	// a half-new binary on disk.
	tmp := filepath.Join(filepath.Dir(self), fmt.Sprintf(".rasterbar.new-%d", os.Getpid()))
	defer os.Remove(tmp) // no-op on success; a leftover must never linger

	// A previous run's leftover (a crashed download) is removed first, or the
	// download opens it to append.
	if err := os.RemoveAll(tmp); err != nil && !os.IsNotExist(err) {
		return "", err
	}

	dl := &http.Client{Timeout: updateDownloadTimeout}
	resp, err := dl.Get(a.URL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", a.Name, resp.Status)
	}
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", tmp, err)
	}
	written, err := io.Copy(out, resp.Body)
	if err != nil {
		out.Close()
		return "", fmt.Errorf("writing to %s: %w", tmp, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	// A binary smaller than its advertised size, or one that is not an ELF, is
	// refused — writing it over the executable is not retry-able from this
	// program having overwritten it with something it cannot run.
	if written != a.Size {
		return "", fmt.Errorf(
			"download %s: got %d bytes, release says %d", a.Name, written, a.Size)
	}
	head := make([]byte, len(elfMagic))
	f, err := os.Open(tmp)
	if err != nil {
		return "", err
	}
	n, err := f.Read(head)
	f.Close()
	if err != nil || n != len(elfMagic) || string(head) != string(elfMagic) {
		return "", fmt.Errorf("download %s: not an ELF binary", a.Name)
	}
	if err := os.Rename(tmp, self); err != nil {
		return "", fmt.Errorf("replace %s: %w", self, err)
	}
	return self, nil
}

// runUpdate checks for a newer release and, without --check, installs it.
// The comparison is on the parsed tag; the version stamp of a hand-built
// binary is empty, and one that isn't comparable cannot claim to know whether
// it is up to date — so it installs.
func runUpdate(checkOnly bool) (string, error) {
	rel, err := fetchLatestRelease()
	if err != nil {
		return "", err
	}
	if checkOnly {
		if version == "" {
			return fmt.Sprintf("development build; %s latest release is %s", updateRepo, rel.TagName), nil
		}
		if newerVersion(version, rel.TagName) {
			return fmt.Sprintf("%s available (installed: %s) — run `rasterbar update`", rel.TagName, version), nil
		}
		return fmt.Sprintf("up to date at %s", version), nil
	}
	if version != "" && !newerVersion(version, rel.TagName) {
		return fmt.Sprintf("up to date at %s (%s is not newer)", version, rel.TagName), nil
	}
	asset, err := pickUpdateAsset(rel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "downloading %s (%s)...", asset.Name, runtime.GOOS+"/"+runtime.GOARCH)
	self, err := installAsset(asset)
	if err != nil {
		return "", err
	}
	var from string
	if version == "" {
		from = "the running build"
	} else {
		from = version
	}
	return fmt.Sprintf("updated %s -> %s at %s. Next rasterbar run uses it.", from, rel.TagName, self), nil
}

// updateNotice returns a channel that later delivers one line of update news,
// or nil when the check is not running. The caller consumes it non-blockingly
// before the UI takes over the terminal — never across a select while frames
// are being painted, because the check would then own stderr's timing.
func updateNotice() <-chan string {
	if os.Getenv("VSPZ_YT_CLI_UPDATE") != "auto" {
		return nil
	}
	// A development build has no version to compare against: announcing
	// "1.2.3 available" on every launch would be nagging with false data.
	if version == "" {
		return nil
	}
	ch := make(chan string, 1)
	go func() {
		rel, err := fetchLatestRelease()
		if err == nil && newerVersion(version, rel.TagName) {
			ch <- fmt.Sprintf("rasterbar: %s available (installed %s) — `rasterbar update`", rel.TagName, version)
		}
	}()
	return ch
}

// drainUpdateNotice reports the update channel's message without waiting.
// If the check has not finished, the playback starts and the news is lost —
// a slow check must cost nothing, and a missed notice costs one launch.
func drainUpdateNotice(ch <-chan string) {
	if ch == nil {
		return
	}
	select {
	case msg := <-ch:
		fmt.Fprintln(os.Stderr, msg)
	default:
	}
}
