package enrich

// standards maps canonical standard IDs to their StandardInfo.
// Keys are the canonical form used by Fusion (typically DIN). Equivalents
// are stored on the value so callers can query multiple vendor catalogs
// without picking a side.
//
// Coverage: the ~25 most common metric fasteners seen in real-world
// assemblies. Extend as needed — each entry is ~10 lines and unit-testable
// via the parser_test.go harness.
var standards = map[string]*StandardInfo{
	// ── Socket head cap screws ──────────────────────────────────────────
	"DIN 912": {
		Standard:    "DIN 912",
		Equivalents: []string{"ISO 4762"},
		Type:        "socket_head_cap_screw",
		DisplayName: "Socket Head Cap Screw",
		Category:    "screw",
		HeadType:    "socket",
		Drive:       "allen",
		Thread:      "full",
		NeedsLength: true,
	},
	"DIN 7984": {
		Standard:    "DIN 7984",
		Type:        "low_head_socket_cap_screw",
		DisplayName: "Low-Profile Socket Head Cap Screw",
		Category:    "screw",
		HeadType:    "socket",
		Drive:       "allen",
		Thread:      "full",
		NeedsLength: true,
	},

	// ── Countersunk / flat-head screws ──────────────────────────────────
	"DIN 7991": {
		Standard:    "DIN 7991",
		Equivalents: []string{"ISO 10642"},
		Type:        "countersunk_socket_head_screw",
		DisplayName: "Countersunk Socket Head Screw (Flat Head)",
		Category:    "screw",
		HeadType:    "countersunk",
		Drive:       "allen",
		Thread:      "full",
		NeedsLength: true,
	},
	"DIN 963": {
		Standard:    "DIN 963",
		Equivalents: []string{"ISO 2009"},
		Type:        "countersunk_slotted_screw",
		DisplayName: "Countersunk Slotted Machine Screw",
		Category:    "screw",
		HeadType:    "countersunk",
		Drive:       "slotted",
		Thread:      "full",
		NeedsLength: true,
	},
	"DIN 965": {
		Standard:    "DIN 965",
		Equivalents: []string{"ISO 7046", "DIN EN ISO 7046-1", "DIN EN ISO 7046-2", "ISO 7046-1", "ISO 7046-2"},
		Type:        "countersunk_phillips_screw",
		DisplayName: "Countersunk Phillips Machine Screw",
		Category:    "screw",
		HeadType:    "countersunk",
		Drive:       "phillips",
		Thread:      "full",
		NeedsLength: true,
	},

	// ── Button head and pan head ────────────────────────────────────────
	"DIN 7380": {
		Standard:    "DIN 7380",
		Equivalents: []string{"ISO 7380", "ISO 7380-1", "ISO 7380-2"},
		Type:        "button_head_socket_screw",
		DisplayName: "Button Head Socket Cap Screw",
		Category:    "screw",
		HeadType:    "button",
		Drive:       "allen",
		Thread:      "full",
		NeedsLength: true,
	},
	"DIN 7985": {
		Standard:    "DIN 7985",
		Equivalents: []string{"ISO 7045"},
		Type:        "pan_head_phillips_screw",
		DisplayName: "Pan Head Phillips Machine Screw",
		Category:    "screw",
		HeadType:    "pan",
		Drive:       "phillips",
		Thread:      "full",
		NeedsLength: true,
	},

	// ── Cylinder / slotted ──────────────────────────────────────────────
	"DIN 84": {
		Standard:    "DIN 84",
		Equivalents: []string{"ISO 1207"},
		Type:        "cylinder_slotted_screw",
		DisplayName: "Cylinder Slotted Machine Screw",
		Category:    "screw",
		HeadType:    "cylinder",
		Drive:       "slotted",
		Thread:      "full",
		NeedsLength: true,
	},

	// ── Hex head bolts ──────────────────────────────────────────────────
	"DIN 933": {
		Standard:    "DIN 933",
		Equivalents: []string{"ISO 4017"},
		Type:        "hex_head_screw_full_thread",
		DisplayName: "Hex Head Screw, Fully Threaded",
		Category:    "bolt",
		HeadType:    "hex",
		Drive:       "hex",
		Thread:      "full",
		NeedsLength: true,
	},
	"DIN 931": {
		Standard:    "DIN 931",
		Equivalents: []string{"ISO 4014"},
		Type:        "hex_head_bolt_partial_thread",
		DisplayName: "Hex Head Bolt, Partially Threaded",
		Category:    "bolt",
		HeadType:    "hex",
		Drive:       "hex",
		Thread:      "partial",
		NeedsLength: true,
	},

	// ── Set screws ──────────────────────────────────────────────────────
	"DIN 913": {
		Standard:    "DIN 913",
		Equivalents: []string{"ISO 4026"},
		Type:        "set_screw_flat_point",
		DisplayName: "Set Screw, Flat Point",
		Category:    "set_screw",
		Drive:       "allen",
		Thread:      "full",
		NeedsLength: true,
	},
	"DIN 914": {
		Standard:    "DIN 914",
		Equivalents: []string{"ISO 4027"},
		Type:        "set_screw_cone_point",
		DisplayName: "Set Screw, Cone Point",
		Category:    "set_screw",
		Drive:       "allen",
		Thread:      "full",
		NeedsLength: true,
	},
	"DIN 915": {
		Standard:    "DIN 915",
		Equivalents: []string{"ISO 4028"},
		Type:        "set_screw_dog_point",
		DisplayName: "Set Screw, Dog Point",
		Category:    "set_screw",
		Drive:       "allen",
		Thread:      "full",
		NeedsLength: true,
	},
	"DIN 916": {
		Standard:    "DIN 916",
		Equivalents: []string{"ISO 4029"},
		Type:        "set_screw_cup_point",
		DisplayName: "Set Screw, Cup Point",
		Category:    "set_screw",
		Drive:       "allen",
		Thread:      "full",
		NeedsLength: true,
	},

	// ── Nuts ────────────────────────────────────────────────────────────
	"DIN 934": {
		Standard:    "DIN 934",
		Equivalents: []string{"ISO 4032"},
		Type:        "hex_nut",
		DisplayName: "Hex Nut",
		Category:    "nut",
		HeadType:    "hex",
	},
	"DIN 985": {
		Standard:    "DIN 985",
		Equivalents: []string{"ISO 10511", "ISO 7040"},
		Type:        "nylock_hex_nut",
		DisplayName: "Prevailing-Torque Nylock Hex Nut",
		Category:    "nut",
		HeadType:    "hex",
	},
	"DIN 6926": {
		Standard:    "DIN 6926",
		Equivalents: []string{"ISO 7043", "ISO 7044"},
		Type:        "prevailing_torque_flange_nut",
		DisplayName: "Prevailing-Torque Hex Flange Nut",
		Category:    "nut",
		HeadType:    "hex",
	},
	"DIN 439": {
		Standard:    "DIN 439",
		Equivalents: []string{"ISO 4035"},
		Type:        "thin_hex_nut",
		DisplayName: "Hex Nut, Thin",
		Category:    "nut",
		HeadType:    "hex",
	},
	"DIN 1587": {
		Standard:    "DIN 1587",
		Type:        "cap_nut",
		DisplayName: "Acorn / Cap Nut",
		Category:    "nut",
		HeadType:    "hex",
	},

	// ── Washers ─────────────────────────────────────────────────────────
	"DIN 125": {
		Standard:    "DIN 125",
		Equivalents: []string{"ISO 7089", "ANSI B18.22M"},
		Type:        "flat_washer",
		DisplayName: "Flat Washer",
		Category:    "washer",
	},
	"DIN 9021": {
		Standard:    "DIN 9021",
		Equivalents: []string{"ISO 7093"},
		Type:        "large_flat_washer",
		DisplayName: "Large Flat Washer (Fender Washer)",
		Category:    "washer",
	},
	"DIN 127": {
		Standard:    "DIN 127",
		Type:        "spring_lock_washer",
		DisplayName: "Spring Lock Washer",
		Category:    "washer",
	},
	"DIN 6798": {
		Standard:    "DIN 6798",
		Type:        "toothed_lock_washer",
		DisplayName: "External Toothed Lock Washer",
		Category:    "washer",
	},
}

// isoAliases maps ISO standards → their DIN canonical form. Populated once
// from `standards` so callers can pass an ISO identifier and still get back
// the same *StandardInfo.
var isoAliases = buildISOAliases()

func buildISOAliases() map[string]string {
	m := make(map[string]string)
	for dinKey, info := range standards {
		for _, eq := range info.Equivalents {
			m[eq] = dinKey
		}
	}
	return m
}

// Lookup returns the StandardInfo for a canonical standard identifier.
// Accepts either the DIN canonical key or any registered equivalent
// (ISO, UNI, etc.). Returns (nil, false) if the standard isn't in the table.
func Lookup(standard string) (*StandardInfo, bool) {
	if info, ok := standards[standard]; ok {
		return info, true
	}
	if din, ok := isoAliases[standard]; ok {
		return standards[din], true
	}
	return nil, false
}

// Standards returns a snapshot of all known standard identifiers.
// Used by tooling (UI, reports) to report coverage.
func Standards() []string {
	out := make([]string, 0, len(standards))
	for k := range standards {
		out = append(out, k)
	}
	return out
}
