package ui

import "testing"

func TestFormatSize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"zero", "0", "0 B"},
		{"small bytes", "512", "512 B"},
		{"kb boundary", "1024", "1.0 KB"},
		{"one and a half kb", "1536", "1.5 KB"},
		{"one mb", "1048576", "1.0 MB"},
		{"one gb", "1073741824", "1.0 GB"},
		{"non numeric returns input as-is", "abc", "abc"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := formatSize(tc.in)
			if got != tc.want {
				t.Errorf("formatSize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
