package api

import "testing"

func TestSetRegion(t *testing.T) {
	orig := region
	t.Cleanup(func() { region = orig })

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty stays empty", input: "", want: ""},
		{name: "US is normalized to empty", input: "US", want: ""},
		{name: "EMEA is preserved", input: "EMEA", want: "EMEA"},
		{name: "AUS is preserved", input: "AUS", want: "AUS"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetRegion(tc.input)
			if region != tc.want {
				t.Errorf("SetRegion(%q): region = %q, want %q", tc.input, region, tc.want)
			}
		})
	}
}
