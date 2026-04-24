package enrich

import "testing"

func TestParseHexHeadScrew(t *testing.T) {
	f, err := Parse("Hexagon Head Screw DIN 933 - M24 x 65")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "desc", "Hexagon Head Screw", f.Description)
	assertEqual(t, "std", "DIN 933", f.Standard)
	assertEqual(t, "thread", "M24", f.Thread)
	if f.Length != 65 {
		t.Errorf("length: want 65, got %d", f.Length)
	}
	if f.Info == nil || f.Info.Type != "hex_head_screw_full_thread" {
		t.Errorf("info: want hex_head_screw_full_thread, got %+v", f.Info)
	}
	wantEquivalents := []string{"ISO 4017"}
	assertStringSlice(t, "equivalents", wantEquivalents, f.Equivalents)
}

func TestParseSocketHeadCapScrew(t *testing.T) {
	f, err := Parse("Hex Socket Head Cap Screw DIN 912 - M6 x 20")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "std", "DIN 912", f.Standard)
	assertEqual(t, "thread", "M6", f.Thread)
	if f.Length != 20 {
		t.Errorf("length: want 20, got %d", f.Length)
	}
	if f.Info == nil || f.Info.Drive != "allen" {
		t.Errorf("info: expected allen drive, got %+v", f.Info)
	}
}

func TestParseHexNutNoLength(t *testing.T) {
	f, err := Parse("Hex Nut DIN 934 - M12")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "std", "DIN 934", f.Standard)
	assertEqual(t, "thread", "M12", f.Thread)
	if f.Length != 0 {
		t.Errorf("length: want 0 for nut, got %d", f.Length)
	}
	if f.Info == nil || f.Info.Category != "nut" {
		t.Errorf("info: expected nut category, got %+v", f.Info)
	}
}

func TestParseWasherNoLength(t *testing.T) {
	f, err := Parse("Washer DIN 125 - M6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "std", "DIN 125", f.Standard)
	if f.Info == nil || f.Info.Category != "washer" {
		t.Errorf("info: expected washer category, got %+v", f.Info)
	}
}

func TestParseISOStandard(t *testing.T) {
	// ISO identifier should resolve to the same DIN entry via isoAliases.
	f, err := Parse("Countersunk Head Screw ISO 10642 - M8 x 25")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "std", "ISO 10642", f.Standard)
	if f.Info == nil || f.Info.Standard != "DIN 7991" {
		t.Errorf("info: expected DIN 7991 via ISO alias, got %+v", f.Info)
	}
}

func TestParseThreadWithPitch(t *testing.T) {
	f, err := Parse("Hexagon Head Screw DIN 933 - M10x1.25 x 30")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "thread", "M10x1.25", f.Thread)
	if f.Length != 30 {
		t.Errorf("length: want 30, got %d", f.Length)
	}
}

func TestParseSpacingVariations(t *testing.T) {
	cases := []string{
		"Hex Head Screw DIN 933 - M8 x 20",
		"Hex Head Screw DIN933 - M8x20",
		"Hex Head Screw DIN 933 M8 x 20", // no dash
	}
	for _, c := range cases {
		f, err := Parse(c)
		if err != nil {
			t.Errorf("unexpected error for %q: %v", c, err)
			continue
		}
		if f.Standard != "DIN 933" {
			t.Errorf("%q: std: want DIN 933, got %q", c, f.Standard)
		}
		if f.Thread != "M8" {
			t.Errorf("%q: thread: want M8, got %q", c, f.Thread)
		}
		if f.Length != 20 {
			t.Errorf("%q: length: want 20, got %d", c, f.Length)
		}
	}
}

func TestParseRejectsNonFastener(t *testing.T) {
	cases := []string{
		"",
		"Wing Assembly",
		"Bearing 6205-2RS",
		"Just some text with no standard",
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("expected error for %q, got none", c)
		}
	}
}

// Real-world descriptions from the Standard Components project.
func TestParseRealWorldWithTrailingMaterial(t *testing.T) {
	cases := []struct {
		raw     string
		stdWant string
		thread  string
		length  int
	}{
		{"Hexagon Head Screw DIN 933 - M22 x 270 Steel 4.6 Plain", "DIN 933", "M22", 270},
		{"Hexagon Socket Head Cap Screw ISO 4762 - M3 x 12 Steel 4.6 Black", "ISO 4762", "M3", 12},
		{"Hexagon Socket Button Head Screw ISO 7380-1 - M2.5 x 4 Steel 4.6 Black", "ISO 7380-1", "M2.5", 4},
		{"Hexagon Socket Set Screw ISO 4029 - M3 x 3 Steel 4.6 Black", "ISO 4029", "M3", 3},
		{"Hexagon Socket Countersunk Head Screw ISO 10642 - M3 x 8 Steel 8.8 Plain", "ISO 10642", "M3", 8},
	}
	for _, c := range cases {
		f, err := Parse(c.raw)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.raw, err)
			continue
		}
		if f.Standard != c.stdWant {
			t.Errorf("%q: std: want %q, got %q", c.raw, c.stdWant, f.Standard)
		}
		if f.Thread != c.thread {
			t.Errorf("%q: thread: want %q, got %q", c.raw, c.thread, f.Thread)
		}
		if f.Length != c.length {
			t.Errorf("%q: length: want %d, got %d", c.raw, c.length, f.Length)
		}
	}
}

func TestParseNutWithPitch(t *testing.T) {
	// "M20 x 2.5" — pitch, not length. Length should be 0.
	f, err := Parse("Hexagon Nut DIN 934 - M20 x 2.5 Steel 6 Plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Standard != "DIN 934" {
		t.Errorf("std: want DIN 934, got %q", f.Standard)
	}
	if f.Thread != "M20x2.5" {
		t.Errorf("thread: want M20x2.5, got %q", f.Thread)
	}
	if f.Length != 0 {
		t.Errorf("length: want 0, got %d", f.Length)
	}
}

func TestParseDINENISO(t *testing.T) {
	f, err := Parse("Countersunk Flat Head Screw DIN EN ISO 7046-1 - M3.5 x 8 - H Steel 4.6 Plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Standard != "DIN EN ISO 7046-1" {
		t.Errorf("std: want DIN EN ISO 7046-1, got %q", f.Standard)
	}
	if f.Thread != "M3.5" {
		t.Errorf("thread: want M3.5, got %q", f.Thread)
	}
	if f.Length != 8 {
		t.Errorf("length: want 8, got %d", f.Length)
	}
}

func TestParseSpringLockWasher(t *testing.T) {
	f, err := Parse("Spring Lock Washer DIN 127 - A 20 Steel 100 HV Plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Standard != "DIN 127" {
		t.Errorf("std: want DIN 127, got %q", f.Standard)
	}
	if f.Thread != "A20" {
		t.Errorf("thread: want A20, got %q", f.Thread)
	}
}

func TestParseBareNumberWasher(t *testing.T) {
	f, err := Parse("Washer ISO 7089 - 3 Steel 100 HV Nickel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Standard != "ISO 7089" {
		t.Errorf("std: want ISO 7089, got %q", f.Standard)
	}
	if f.Size != 3 {
		t.Errorf("size: want 3, got %v", f.Size)
	}
}

func TestLookupAcceptsBothForms(t *testing.T) {
	if _, ok := Lookup("DIN 933"); !ok {
		t.Error("Lookup(DIN 933) should succeed")
	}
	if _, ok := Lookup("ISO 4017"); !ok {
		t.Error("Lookup(ISO 4017) should succeed via alias")
	}
	if _, ok := Lookup("MADE UP 999"); ok {
		t.Error("Lookup(MADE UP 999) should fail")
	}
}

// ---------------------------------------------------------------------------

func assertEqual(t *testing.T, field, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %q, got %q", field, want, got)
	}
}

func assertStringSlice(t *testing.T, field string, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: want %v, got %v", field, want, got)
		return
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("%s[%d]: want %q, got %q", field, i, want[i], got[i])
		}
	}
}
