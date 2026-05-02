package api

import "testing"

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty",
			input: "",
			want:  "",
		},
		{
			name:  "plain alphanumeric",
			input: "MyDesign",
			want:  "MyDesign",
		},
		{
			name:  "space and dot are allowed",
			input: "My Design v2.0",
			want:  "My Design v2.0",
		},
		{
			name:  "path traversal — slashes replaced, dots kept",
			input: "../../etc/passwd",
			want:  ".._.._etc_passwd",
		},
		{
			name:  "non-ASCII letters replaced",
			input: "Caractères Spéciaux",
			want:  "Caract_res Sp_ciaux",
		},
		{
			name:  "all slashes become underscores (TrimSpace does not strip _)",
			input: "////",
			want:  "____",
		},
		{
			name:  "leading and trailing whitespace trimmed",
			input: "  spaces  ",
			want:  "spaces",
		},
		{
			name:  "null byte replaced",
			input: "with\x00null",
			want:  "with_null",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeFilename(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
