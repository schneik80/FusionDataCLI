package fusion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeProjectID(t *testing.T) {
	// A payload that base64-encodes differently in std vs URL-safe encoding.
	// "foo:bar#baz" -> bytes that map to a `+` or `/` in std encoding when
	// permuted. We pick a payload where std encoding differs from URL-safe.
	stdPayload := "\xfb\xff?foo#bar" // bytes guaranteed to produce + and / in std encoding
	stdEncoded := base64.RawStdEncoding.EncodeToString([]byte(stdPayload))
	urlEncoded := base64.RawURLEncoding.EncodeToString([]byte(stdPayload))
	if stdEncoded == urlEncoded {
		t.Fatalf("test setup: expected std and URL-safe encodings to differ, got %q == %q", stdEncoded, urlEncoded)
	}

	// Self-encoded payload for the documented assertion in the spec.
	selfPayload := "business:autodesk#12345"
	selfEncoded := "a." + base64.RawURLEncoding.EncodeToString([]byte(selfPayload))

	// "justatag" with no '#'
	noHash := "a." + base64.RawURLEncoding.EncodeToString([]byte("justatag"))

	// "foo#" - ends in '#' with no id
	endHash := "a." + base64.RawURLEncoding.EncodeToString([]byte("foo#"))

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "valid url-safe base64 from spec",
			in:   "a.YnVzaW5lc3M6YXV0b2Rlc2s4MDgzIzIwMjUwMjEzODc2NjAyNTMx",
			want: "20250213876602531",
		},
		{
			name: "self-encoded url-safe",
			in:   selfEncoded,
			want: "12345",
		},
		{
			name: "missing a. prefix",
			in:   "b.something",
			want: "",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "garbage base64",
			in:   "a.!!!notbase64!!!",
			want: "",
		},
		{
			name: "decoded payload missing #",
			in:   noHash,
			want: "",
		},
		{
			name: "decoded payload ends in # with no id",
			in:   endHash,
			want: "",
		},
		{
			name: "std-base64 fallback decodes when url-safe fails",
			in:   "a." + stdEncoded,
			want: "bar",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeProjectID(tc.in)
			if got != tc.want {
				t.Errorf("NormalizeProjectID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidFileID(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		// Accepted
		{"lineage urn", "urn:adsk.wipprod:dm.lineage:hC6gVxs6QYC6OWpnQNd7Ow", true},
		{"a. prefixed b64", "a.YnVzaW5lc3M6YXV0b2Rlc2s", true},
		{"plain alnum", "abc123", true},
		{"all allowed chars", "A-Z_0.9:colons", true},

		// Rejected
		{"empty", "", false},
		{"single space", " ", false},
		{"contains space", "foo bar", false},
		{"contains slash", "foo/bar", false},
		{"contains single quote", "foo'bar", false},
		{"contains double quote", "foo\"bar", false},
		{"contains backslash", "foo\\bar", false},
		{"contains newline", "foo\nbar", false},
		{"contains tab", "foo\tbar", false},
		{"unicode chars", "caractères", false},
		{"contains paren", "foo)bar", false},
		{"contains semicolon", "foo;bar", false},
		{"contains dollar", "foo$bar", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validFileID.MatchString(tc.input)
			if got != tc.want {
				t.Errorf("validFileID.MatchString(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestBuildInsertScript_JSONEscaping(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"normal lineage urn", "urn:adsk.wipprod:dm.lineage:abc123"},
		{"plain", "plain"},
		{"empty", ""},
		{"with double quote", "with\"quote"},
		{"with backslash", "with\\backslash"},
		{"with newline", "with\nnewline"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := buildInsertScript(tc.input)

			marker := "file_id = "
			start := strings.Index(script, marker)
			if start < 0 {
				t.Fatalf("buildInsertScript output missing %q marker:\n%s", marker, script)
			}
			afterMarker := script[start+len(marker):]
			end := strings.Index(afterMarker, "\n")
			if end < 0 {
				t.Fatalf("buildInsertScript output missing newline after marker:\n%s", script)
			}
			found := afterMarker[:end]

			var out string
			if err := json.Unmarshal([]byte(found), &out); err != nil {
				t.Fatalf("found literal %q is not valid JSON string: %v", found, err)
			}
			if out != tc.input {
				t.Errorf("round-trip: decoded %q from script literal %q, want %q", out, found, tc.input)
			}
		})
	}
}

func TestParseToolErrorText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"success false with error", `{"success":false,"error":"oops"}`, "oops"},
		{"success false no error", `{"success":false}`, "tool reported failure"},
		{"success true", `{"success":true}`, ""},
		{"success true with error ignored", `{"success":true,"error":"ignored"}`, ""},
		{"plain text not json", "plain text not json", ""},
		{"json array starts with bracket", "[1,2,3]", ""},
		{"empty", "", ""},
		{"trim spaces around json", `  {"success":false,"error":"trim me"}  `, "trim me"},
		{"malformed json", "{garbage", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseToolErrorText(tc.in)
			if got != tc.want {
				t.Errorf("parseToolErrorText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractSSEData(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single data line", "data: {\"foo\":1}\n", `{"foo":1}`},
		{"multiple data lines concat", "data: {\"a\":1}\ndata: {\"b\":2}\n", `{"a":1}{"b":2}`},
		{"event prefix then data", "event: ping\ndata: {\"x\":true}\n\n", `{"x":true}`},
		{"crlf line endings", "data: {\"v\":1}\r\n", `{"v":1}`},
		{"no sse framing returns input", "plain body", "plain body"},
		{"empty returns empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(extractSSEData([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("extractSSEData(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// invalidFileIDCases lists fileId values that should be rejected by the public
// OpenDocument / InsertDocument validators *before* any network I/O happens.
// The tests below use a sentinel endpoint that would refuse to connect — so a
// connect-error in place of a validation-error is a regression.
var invalidFileIDCases = []struct {
	name string
	id   string
}{
	{"empty", ""},
	{"with space", "with space"},
	{"shell metachars", "foo;rm -rf"},
	{"single quote", "foo'bar"},
	{"newline", "with\nnewline"},
	{"unicode", "caractères"},
}

// tripwireClient returns a Client whose endpoint will refuse connections,
// so that any code path which tries to dial out fails the test.
func tripwireClient() *Client {
	return &Client{Endpoint: "http://127.0.0.1:1/should-not-be-called"}
}

func assertValidationRejection(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil (validation did not run)", name)
	}
	msg := err.Error()
	if !(strings.Contains(msg, "fileId") || strings.Contains(msg, "empty") || strings.Contains(msg, "invalid")) {
		t.Fatalf("%s: error %q does not look like a validation rejection — validation may not be running before dial", name, msg)
	}
}

func TestOpenDocument_ValidatesInput(t *testing.T) {
	client := tripwireClient()
	for _, tc := range invalidFileIDCases {
		t.Run(tc.name, func(t *testing.T) {
			err := client.OpenDocument(context.Background(), tc.id)
			assertValidationRejection(t, "OpenDocument("+tc.name+")", err)
		})
	}
}

func TestInsertDocument_ValidatesInput(t *testing.T) {
	client := tripwireClient()
	for _, tc := range invalidFileIDCases {
		t.Run(tc.name, func(t *testing.T) {
			err := client.InsertDocument(context.Background(), tc.id)
			assertValidationRejection(t, "InsertDocument("+tc.name+")", err)
		})
	}
}
