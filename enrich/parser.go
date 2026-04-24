package enrich

import (
	"regexp"
	"strconv"
	"strings"
)

// fastenerRe matches Fusion library fastener descriptions. Real examples
// from the Standard Components project:
//
//	"Hexagon Head Screw DIN 933 - M22 x 270 Steel 4.6 Plain"
//	"Hexagon Socket Head Cap Screw ISO 4762 - M3 x 12 Steel 4.6 Black"
//	"Countersunk Flat Head Screw DIN EN ISO 7046-1 - M3.5 x 8 - H Steel 4.6 Plain"
//	"Hexagon Nut DIN 934 - M20 x 2.5 Steel 6 Plain"
//	"Spring Lock Washer DIN 127 - A 20 Steel 100 HV Plain"
//	"Hexagon Socket Set Screw ISO 4029 - M3 x 3 Steel 4.6 Black"
//
// Capture groups:
//
//	desc   — prose prefix before the standard
//	std    — "DIN 933" | "ISO 4017" | "DIN EN ISO 7046-1" | "ANSI B18.2.1"
//	thread — "M24" | "M3.5" | "M20x2.5" (pitch glued on) | "A20"
//	length — optional; the first integer after " x " *following* size
//
// Material / grade / finish after the length are left for a second pass.
var fastenerRe = regexp.MustCompile(
	`^(?P<desc>.+?)[\s,]+` +
		`(?P<std>(?:DIN(?:\s+EN)?(?:\s+ISO)?|ISO|ANSI|ASME|EN|UNI|JIS)\s*[A-Z]?\s*\d+(?:[.-]\d+)*[A-Za-z]?)` +
		`\s*[-–—:]?\s*` +
		`(?P<thread>[MGA]\s*\d+(?:\.\d+)?(?:\s*[xX×]\s*\d+\.\d+)?)` +
		`(?:\s*[xX×]\s*(?P<length>\d+))?` +
		`(?:[\s-].*)?$`,
)

// washerRe covers washer/pin descriptions that use a bare number for the
// nominal size instead of an M-prefixed thread:
//
//	"Washer ISO 7089 - 3 Steel 100 HV Nickel"
//	"Plain Washer ANSI B18.22M - 3 N Steel 100 HV Plain"
var washerRe = regexp.MustCompile(
	`^(?P<desc>.+?)[\s,]+` +
		`(?P<std>(?:DIN(?:\s+EN)?(?:\s+ISO)?|ISO|ANSI|ASME|EN|UNI|JIS)\s*[A-Z]?\s*\d+(?:[.-]\d+)*[A-Za-z]?)` +
		`\s*[-–—:]?\s*` +
		`(?P<size>\d+(?:\.\d+)?)` +
		`(?:[\s-].*)?$`,
)

// Parse extracts a NormalizedFastener from a Fusion component description.
// The returned fastener's Info is populated if the standard is in the local
// lookup table. Callers can then merge in material + appearance separately.
//
// Returns a *ParseError if the description doesn't match either fastener
// pattern — callers should skip non-fastener components on that error
// rather than fail the whole pipeline.
func Parse(description string) (*NormalizedFastener, error) {
	raw := strings.TrimSpace(description)
	if raw == "" {
		return nil, &ParseError{Raw: raw, Reason: "empty description"}
	}

	// Try the primary threaded-fastener pattern first.
	if m := fastenerRe.FindStringSubmatch(raw); m != nil {
		pick := groupPicker(fastenerRe, m)
		f := &NormalizedFastener{
			Raw:         raw,
			Description: pick("desc"),
			Standard:    normalizeStandard(pick("std")),
			Thread:      normalizeThread(pick("thread")),
		}
		if s := pick("length"); s != "" {
			if v, err := strconv.Atoi(s); err == nil {
				f.Length = v
			}
		}
		attachInfo(f)
		return f, nil
	}

	// Fallback: washer / pin with a bare-number nominal size.
	if m := washerRe.FindStringSubmatch(raw); m != nil {
		pick := groupPicker(washerRe, m)
		f := &NormalizedFastener{
			Raw:         raw,
			Description: pick("desc"),
			Standard:    normalizeStandard(pick("std")),
		}
		if s := pick("size"); s != "" {
			if v, err := strconv.ParseFloat(s, 64); err == nil {
				f.Size = v
			}
		}
		attachInfo(f)
		return f, nil
	}

	return nil, &ParseError{Raw: raw, Reason: "no DIN/ISO/ANSI pattern match"}
}

func attachInfo(f *NormalizedFastener) {
	if info, ok := Lookup(f.Standard); ok {
		f.Info = info
		f.Equivalents = info.Equivalents
	}
}

func groupPicker(re *regexp.Regexp, m []string) func(string) string {
	names := re.SubexpNames()
	return func(group string) string {
		for i, n := range names {
			if n == group {
				return strings.TrimSpace(m[i])
			}
		}
		return ""
	}
}

// normalizeStandard collapses whitespace, uppercases the prefix, and ensures
// a single space separates prefix letters from digits. Handles multi-word
// prefixes like "DIN EN ISO".
//
//	"DIN  933"         → "DIN 933"
//	"iso4017"          → "ISO 4017"
//	"DIN EN ISO 7046-1" → "DIN EN ISO 7046-1"  (unchanged)
func normalizeStandard(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`^([A-Z]+)(\d)`).ReplaceAllString(s, "$1 $2")
	return s
}

// normalizeThread strips whitespace, uppercases the prefix, and lowercases
// the separator:
//
//	"m6"          → "M6"
//	"M 6 x 1"     → "M6x1"
//	"M10 x 1.25"  → "M10x1.25"
//	"A 20"        → "A20"
func normalizeThread(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "X", "x")
	s = strings.ReplaceAll(s, "×", "x")
	return s
}
