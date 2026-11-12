package main

import (
	"strings"
	"testing"

	"github.com/schneik80/FusionDataCLI/api"
)

func TestPickHub(t *testing.T) {
	hubs := []api.NavItem{
		{ID: "h1", Name: "Acme"},
		{ID: "h2", Name: "Acme Robotics"},
		{ID: "h3", Name: "Industrial Machine Arts LLC"},
	}
	cases := []struct {
		want, id, errHas string
	}{
		{"Acme", "h1", ""},          // exact name wins over the longer match
		{"acme robotics", "h2", ""}, // case-insensitive
		{"h3", "h3", ""},            // id
		{"machine arts", "h3", ""},  // unique fragment
		{"Robot", "h2", ""},
		{"Ac", "", "more than one hub"},
		{"Nope", "", "no hub matches"},
		{"", "", "-hub is required"},
	}
	for _, c := range cases {
		got, err := pickHub(hubs, c.want)
		switch {
		case c.errHas != "":
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("pickHub(%q): err = %v, want %q", c.want, err, c.errHas)
			}
		case err != nil || got.ID != c.id:
			t.Errorf("pickHub(%q) = %q, %v; want %q", c.want, got.ID, err, c.id)
		}
	}
	// With a single hub the flag is optional.
	if got, err := pickHub(hubs[:1], ""); err != nil || got.ID != "h1" {
		t.Errorf("single hub: %q, %v", got.ID, err)
	}
	if _, err := pickHub(nil, ""); err == nil {
		t.Error("no hubs: want an error")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Industrial Machine Arts LLC": "industrial-machine-arts-llc",
		"  A/B: c?  ":                 "a-b-c",
		"ADSK-Schneik":                "adsk-schneik",
		"!!!":                         "hub",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
