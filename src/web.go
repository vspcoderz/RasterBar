package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// The web and app verbs.
//
// Both serve the same loopback UI; app only changes the window: a Chromium
// --app instance with its own profile, so the toolbar and tabs are gone and the
// thing looks like a program rather than a page. Electron and Tauri were both
// rejected for the same reason: a second runtime, a second build system and a
// second update path for a window the browser already knows how to draw.

const defaultWebPort = 8420

func webSessionToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func runWeb(opts options, cfg settings, mode ColorMode) int {
	cacheDir, err := resolveCacheDir(opts.cacheDir, cfg.CacheDir)
	if err != nil {
		// Not fatal: the player works without a cache, it just re-downloads.
		fmt.Fprintf(os.Stderr, "cache: %v (downloads disabled)\n", err)
		cacheDir = ""
	}

	token, err := webSessionToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "web: cannot mint a session token: %v\n", err)
		return 1
	}
	eng := newWebEngine(opts, cfg, cacheDir, mode, version)
	defer eng.Close()

	host := opts.webHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := opts.webPort
	if port == 0 && !opts.webPortSet {
		port = defaultWebPort
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil && port != 0 {
		fmt.Fprintf(os.Stderr, "web: port %d is busy (%v); using a free port\n", port, err)
		ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "web: %v\n", err)
		return 1
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	display := net.JoinHostPort(host, strconv.Itoa(addr.Port))
	url := "http://" + display + "/?token=" + token
	srv := newWebServer(eng, token, host, addr.Port)
	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	fmt.Fprintf(os.Stderr, "rasterbar %s on http://%s\n", opts.webVerb, display)
	if opts.webNoOpen {
		fmt.Printf("%s\n", url)
	} else {
		if err := openWebTarget(url, opts.appMode, opts.webBrowser); err != nil {
			fmt.Fprintf(os.Stderr, "could not open a window: %v\nopen this URL instead:\n%s\n", err, url)
		} else if opts.appMode {
			fmt.Fprintln(os.Stderr, "app window opened")
		} else {
			fmt.Fprintln(os.Stderr, "browser opened")
		}
	}
	fmt.Fprintln(os.Stderr, "stop with Ctrl-C")

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "web: %v\n", err)
			return 1
		}
	case <-ctx.Done():
	}

	// Close the engine first: it closes every event stream's done channel, so
	// Shutdown is not waiting on connections that are designed to be infinite.
	eng.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdown)
	return 0
}

// chromelessBrowsers is deliberately Chromium-only. --app is a Chromium flag;
// handing it to Firefox starts a browser that ignores it and opens a tab with a
// URL that looks like a flag, which is worse than saying so.
var chromelessBrowsers = []string{
	"chromium", "chromium-browser", "google-chrome", "google-chrome-stable",
	"brave-browser", "brave", "microsoft-edge", "microsoft-edge-stable",
}

func findBrowser(preferred string, names []string) (string, error) {
	if preferred != "" {
		p, err := exec.LookPath(preferred)
		if err != nil {
			return "", fmt.Errorf("%s not found", preferred)
		}
		return p, nil
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", errors.New("none of " + joinWords(names) + " found")
}

func joinWords(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

func openWebTarget(url string, app bool, preferred string) error {
	if app {
		path, err := findBrowser(preferred, chromelessBrowsers)
		if err != nil {
			return fmt.Errorf("app mode needs a Chromium-based browser: %v (try `rasterbar web`)", err)
		}
		profile := ""
		if base, err := os.UserConfigDir(); err == nil && base != "" {
			profile = filepath.Join(base, "rasterbar", "app-profile")
			if err := os.MkdirAll(profile, 0o700); err != nil {
				return fmt.Errorf("app profile: %w", err)
			}
		}
		args := []string{
			"--app=" + url,
			"--class=RasterBar",
			"--name=RasterBar",
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-search-engine-choice-screen",
			"--password-store=basic",
			"--window-size=1440,900",
			"--ozone-platform-hint=auto",
		}
		if profile != "" {
			args = append(args, "--user-data-dir="+profile)
		}
		return startDetached(path, args...)
	}

	if preferred != "" {
		path, err := findBrowser(preferred, nil)
		if err != nil {
			return err
		}
		return startDetached(path, url)
	}
	if path, err := exec.LookPath("xdg-open"); err == nil {
		return startDetached(path, url)
	}
	path, err := findBrowser("", append(chromelessBrowsers, "firefox"))
	if err != nil {
		return err
	}
	return startDetached(path, "--new-window", url)
}

func startDetached(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	// The window outlives this call, and its output is not ours to parse.
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
