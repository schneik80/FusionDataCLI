// Package enrich contains the Fasteners Enrichment pipeline logic — parsing
// Fusion descriptions into normalized specs, looking up matching vendor
// candidates, and preparing write-back payloads for the MFG v3 API.
//
// This package deliberately has zero API dependencies so the matching logic
// can be unit-tested without network calls. API wiring lives in `web/` (the
// `serve` subcommand).
package enrich

// NormalizedFastener is the result of parsing a Fusion component description
// and merging in per-component material / appearance context.
//
// Exactly one of {Thread, Size} is populated depending on the fastener class:
//   - Threaded fasteners (screws, bolts, nuts): Thread = "M6", "M24", "G1/2"
//   - Non-threaded (washers, pins): Size = nominal inside diameter in mm
//
// Length is zero for nuts, washers, and non-length-specified parts.
type NormalizedFastener struct {
	Raw         string // original description, preserved for round-trip debug
	Description string // prose prefix, e.g. "Hexagon Head Screw"

	Standard    string   // canonical e.g. "DIN 933" (preferred form if multiple given)
	Equivalents []string // e.g. ["ISO 4017"] — populated by standards lookup, not parsing

	Thread string  // e.g. "M24", "M6", "G1/2"; empty for non-threaded
	Size   float64 // nominal size in mm for non-threaded parts (washer inner dia); 0 otherwise
	Length int     // length in mm; 0 if not specified (nuts, washers)

	// Context merged from Fusion component properties, not the description.
	Material   string // e.g. "Steel, AISI 4140"
	Appearance string // e.g. "Zinc Plated"

	// Classification, filled by standards lookup.
	Info *StandardInfo // nil if Standard not in the table
}

// StandardInfo describes one fastener standard (DIN/ISO/ANSI/...).
// Multiple standards may map to the same physical part type, which is why
// Equivalents is populated — use it when querying vendors.
type StandardInfo struct {
	Standard    string   // canonical ID e.g. "DIN 933"
	Equivalents []string // e.g. ["ISO 4017", "UNI 5739"]
	Type        string   // slug e.g. "hex_head_screw_full_thread"
	DisplayName string   // e.g. "Hex Head Screw, Fully Threaded"
	Category    string   // "screw" | "bolt" | "nut" | "washer" | "set_screw" | "pin"
	HeadType    string   // "hex" | "socket" | "countersunk" | "button" | "pan" | "cylinder" | ""
	Drive       string   // "hex" | "allen" | "phillips" | "slotted" | "torx" | ""
	Thread      string   // "full" | "partial" | "" (for non-threaded)
	NeedsLength bool     // screws/bolts/pins yes; nuts/washers no
}

// ParseError indicates the description didn't match any recognized pattern.
// Callers typically treat this as "skip this component, not a fastener".
type ParseError struct {
	Raw    string
	Reason string
}

func (e *ParseError) Error() string {
	return "unparseable fastener description: " + e.Raw + " — " + e.Reason
}
