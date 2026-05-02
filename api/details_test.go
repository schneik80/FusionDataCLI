package api

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	mustParse := func(layout, s string) time.Time {
		t.Helper()
		v, err := time.Parse(layout, s)
		if err != nil {
			t.Fatalf("setup: parsing %q with %q: %v", s, layout, err)
		}
		return v
	}

	cases := []struct {
		name     string
		input    string
		wantZero bool
		want     time.Time
	}{
		{
			name:     "empty string returns zero time",
			input:    "",
			wantZero: true,
		},
		{
			name:  "RFC3339 UTC",
			input: "2024-01-15T10:30:45Z",
			want:  mustParse(time.RFC3339, "2024-01-15T10:30:45Z"),
		},
		{
			name:  "fractional Z fallback",
			input: "2024-01-15T10:30:45.123Z",
			want:  mustParse("2006-01-02T15:04:05.000Z", "2024-01-15T10:30:45.123Z"),
		},
		{
			name:  "RFC3339 with positive offset",
			input: "2024-01-15T10:30:45+02:00",
			want:  mustParse(time.RFC3339, "2024-01-15T10:30:45+02:00"),
		},
		{
			name:     "garbage returns zero time",
			input:    "garbage",
			wantZero: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTime(tc.input)
			if tc.wantZero {
				if !got.IsZero() {
					t.Errorf("parseTime(%q) = %v, want zero time", tc.input, got)
				}
				return
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseTime(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestApiUser_FullName(t *testing.T) {
	cases := []struct {
		name  string
		first string
		last  string
		want  string
	}{
		{name: "both empty", first: "", last: "", want: ""},
		{name: "first only", first: "Ada", last: "", want: "Ada"},
		{name: "last only", first: "", last: "Lovelace", want: "Lovelace"},
		{name: "both present", first: "Ada", last: "Lovelace", want: "Ada Lovelace"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := apiUser{First: tc.first, Last: tc.last}
			got := u.fullName()
			if got != tc.want {
				t.Errorf("apiUser{%q,%q}.fullName() = %q, want %q", tc.first, tc.last, got, tc.want)
			}
		})
	}
}
