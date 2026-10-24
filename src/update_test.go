package main

import (
	"strings"
	"testing"
)

// update_test.go covers the comparisons and asset matching; the actual
// download-and-swap is the manual tier (network + filesystem replacement of a
// running binary).

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"v1.2.3", []int{1, 2, 3}},
		{"1.2.3", []int{1, 2, 3}},
		{"v0", []int{0}},
		{"v1.3-1", []int{1, 3}},
		{"", nil},
		{"dev", nil},
	}
	for _, c := range cases {
		got := parseVersion(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseVersion(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseVersion(%q)[%d] = %d, want %d", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.10.0", "v1.9.0", false}, // component-wise, not string-wise
		{"v1.9", "v1.9.1", true},     // a missing component compares as zero
		{"v1.9.1", "v1.9", false},
		{"v1.2.3", "v1.2.3", false}, // equal is up to date
		{"", "v1.2.3", false},       // a dev build cannot be compared
		{"v1.2.3", "", false},
		{"dev", "v1.2.3", false}, // unparsable never claims update
	}
	for _, c := range cases {
		if got := newerVersion(c.current, c.latest); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestPickUpdateAsset(t *testing.T) {
	rel := &ghRelease{TagName: "v9.0.0", Assets: []ghAsset{
		{Name: "rasterbar-9.0.0-linux-arm64.tar.gz"},
		{Name: "RASPERBAR-9.0.0-LINUX-AMD64", URL: "https://dl/one"}, // case-insensitive on purpose
		{Name: "rasterbar-9.0.0-windows-amd64.exe"},
	}}
	a, err := pickUpdateAsset(rel, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if a.URL != "https://dl/one" {
		t.Errorf("picked %s", a.Name)
	}
	if _, err := pickUpdateAsset(rel, "linux", "riscv"); err == nil {
		t.Error("a missing arch matched something")
	}
	// The error must show what WAS in the release: "no asset" is a claim
	// about this particular release, and the reader can verify it.
	if _, err := pickUpdateAsset(&ghRelease{TagName: "v9.0.0"}, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Errorf("empty release error = %v", err)
	}
}
