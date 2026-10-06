// Package main is RasterBar: a terminal YouTube player.
//
// It plays audio through mpv and paints the picture itself, straight into the
// terminal's cell grid — ASCII in monochrome, truecolour via half-blocks in
// colour, and in music mode an audio-reactive visualiser in place of the video.
//
// # Two modes, one player
//
// Music mode (-M, the default) drops the video entirely and draws a visualiser.
// Video mode (-a) plays ASCII or colour video with a spectrum strip under it.
// Music mode is a bool on trackSession rather than a second player: both share
// the render loop, the jump prompt, the resize path, the stall handler and the
// queue. They differ in exactly one structural way — where the position comes
// from. Video mode has two children on two clocks and counts frames; music mode
// has one child and asks it.
//
// # Zero third-party modules
//
// This is the project's defining constraint, not a preference. The FFT, the PTY,
// raw mode and termios are each hand-rolled because each dependency would have
// been a few dozen lines of syscall or arithmetic. Nothing outside the standard
// library is imported, anywhere.
//
// # Layout
//
// Everything is in this one package on purpose. The pieces are coupled through
// state that is deliberately unexported — the analyser's stdDb and levelDbfs
// exist so tests can assert the noise gate's decision rather than wait out the
// smoother's release; the renderers' cursor and SGR bookkeeping is read directly
// by the leak suite. Splitting into internal/ packages would mean exporting all
// of it to satisfy the compiler and would buy navigability that splitting the
// files by seam already provides.
//
// One subsystem is already a package of its own: internal/term, which
// knows nothing about tracks, frames, keys or colour. The rest is still
// one package, deliberately -- see above.
//
// The seams within it are:
//
//	session.go   trackSession, the decoder goroutine, the render loop
//	player.go    transport state machine, key table, the : prompt
//	sync.go      SyncPlayer: ffmpeg video pipe + mpv audio, A/V sync
//	ipc.go       mpv JSON IPC
//	hud.go       title / progress bar / footer / spectrum strip formatting
//	render.go    monochrome diff renderer + overlay layout
//	color.go     colour renderer, glyph probe, terminal detection
//	viz.go       VizGrid, style registry, preferences
//	styles_*.go  the six visualisers, one per file
//	palette.go   the ten palettes and the HSV arithmetic behind them
//	fft.go       radix-2 FFT, log-spaced bands, raw vs smoothed magnitudes
//	beat.go      spectral flux, onsets, tempo estimate
//	visual.go    LevelTap (PCM tap) and the display smoother
//	library.go   directory scan; search.go   yt-dlp/ytfzf search
//	tui.go       browse list; flow.go   covered-terminal stall handling
//
//	internal/term
//	            raw-terminal layer: termios, TIOCGWINSZ, and a pty for
//	            subprocesses that insist on having one
//
// AGENTS.md is the rulebook: the conventions, the dead ends not to retry, and
// the units. It is written for the next person to change this, which is usually
// whoever is about to repeat one of these mistakes.
package main
