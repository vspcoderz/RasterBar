package main

import "strings"

// naturalsort.go orders filenames the way a person reads them.
//
// sort.Strings puts "track 10.mp3" before "track 2.mp3", which is the same reason
// sortLibrary parses episode numbers at all: a playlist nobody can scan is not a
// playlist. Episode numbers are solved by parsing them. A music library has no
// episode numbers to parse -- the files are just names -- so the comparison has
// to find the digits inside the name itself.
//
// Scoped to what a media folder actually contains rather than a full numeric
// collation: runs of ASCII digits compare as numbers, everything else compares as
// lowercase text. That is enough to put "celestial" before "nocturne" and
// "Track 2" before "Track 10", and it does not try to be strcoll.

// naturalLess reports whether a sorts before b in natural order.
//
// Digits compare numerically, and a digit sorts before any letter or symbol. The
// second half of that is what makes "02 - Prelude" and "1 - Overture" behave:
// read as text, "0" < "1" is luck and "02 -" < "1 -" is a coincidence of the
// punctuation. Read as numbers it is simply right.
func naturalLess(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ac, bc := a[ai], b[bi]
		switch {
		case isDigit(ac) && isDigit(bc):
			// Compare the whole run as one number. Leading zeros are compared on
			// length as a tiebreak, so "01" and "1" are equal in value but keep a
			// stable relative order instead of flipping between runs.
			as, ae := digitRun(a, ai)
			bs, be := digitRun(b, bi)
			av, bv := trimZeros(a[as:ae]), trimZeros(b[bs:be])
			if len(av) != len(bv) {
				return len(av) < len(bv)
			}
			if av != bv {
				return av < bv
			}
			if ae-as != be-bs {
				return ae-as < be-bs
			}
			ai, bi = ae, be
		case isDigit(ac) != isDigit(bc):
			return isDigit(ac)
		default:
			al, bl := lower(ac), lower(bc)
			if al != bl {
				return al < bl
			}
			ai++
			bi++
		}
	}
	// One ran out first. Equal prefixes are a tie broken by length, which also
	// makes a shorter name sort first when one is a prefix of the other.
	return len(a)-ai < len(b)-bi
}

func digitRun(s string, i int) (start, end int) {
	start = i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return start, i
}

// trimZeros drops leading zeros so "007" and "7" compare equal in value. The
// result is a subslice, which is why the length comparison above is meaningful:
// both sides have been trimmed the same way.
func trimZeros(s string) string {
	i := 0
	for i < len(s)-1 && s[i] == '0' {
		i++
	}
	return s[i:]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// lower is ASCII-only on purpose. Unicode case folding needs tables and a
// language tag to get right, and a terminal is not the place to be wrong about
// it: the two accented characters that matter here fold the same way either way.
func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// naturalJoin is naturalLess over a path, case-folded and with separators
// normalised, so "Album/track 2" and "album/Track 10" sort as a human expects.
func naturalJoin(less func(a, b string) bool) func(a, b string) bool {
	return func(a, b string) bool {
		return less(normalFold(a), normalFold(b))
	}
}

func normalFold(s string) string { return strings.ToLower(filepathToSlash(s)) }

func filepathToSlash(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			out = append(out, '/')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}
