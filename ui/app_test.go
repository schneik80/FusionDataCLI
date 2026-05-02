package ui

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/schneik80/FusionDataCLI/api"
	"github.com/schneik80/FusionDataCLI/auth"
	"github.com/schneik80/FusionDataCLI/fusion"
	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

// TestUpdate_TokenReadyMsg_TransitionsToLoading drives the model from the
// pre-auth waiting state into the hub-loading state by feeding it a
// successful tokenReadyMsg. It locks down the contract that a non-empty
// token transitions to stateLoading + hubLoading and dispatches a hub-load
// command.
func TestUpdate_TokenReadyMsg_TransitionsToLoading(t *testing.T) {
	m := Model{
		state:        stateLoading,
		spinner:      spinner.New(),
		styleCache:   &styleCache{},
		detailsCache: map[string]*api.ItemDetails{},
	}
	updated, cmd := m.Update(tokenReadyMsg{token: "abc"})
	um, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update did not return Model, got %T", updated)
	}
	if um.token != "abc" {
		t.Errorf("token = %q, want %q", um.token, "abc")
	}
	if um.state != stateLoading {
		t.Errorf("state = %d, want stateLoading (%d)", um.state, stateLoading)
	}
	if !um.hubLoading {
		t.Errorf("hubLoading = false, want true")
	}
	if cmd == nil {
		t.Errorf("cmd = nil, want non-nil load-hubs cmd")
	}
}

// TestUpdate_TokenReadyMsg_EmptyTokenGoesAuthNeeded confirms the auth
// fall-through: when checkTokensCmd reports no token, the UI moves to
// stateAuthNeeded and emits no command.
func TestUpdate_TokenReadyMsg_EmptyTokenGoesAuthNeeded(t *testing.T) {
	m := Model{
		state:        stateLoading,
		spinner:      spinner.New(),
		styleCache:   &styleCache{},
		detailsCache: map[string]*api.ItemDetails{},
	}
	updated, cmd := m.Update(tokenReadyMsg{token: ""})
	um, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update did not return Model, got %T", updated)
	}
	if um.state != stateAuthNeeded {
		t.Errorf("state = %d, want stateAuthNeeded (%d)", um.state, stateAuthNeeded)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

// TestUpdate_KeyQuit asserts that pressing q in the browsing state
// returns tea.Quit, which evaluates to a tea.QuitMsg{} when invoked.
func TestUpdate_KeyQuit(t *testing.T) {
	m := sampleBrowsingModel(120, 40)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatalf("cmd = nil, want tea.Quit")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("cmd() returned %T, want tea.QuitMsg", msg)
	}
}

// TestUpdate_NavigateRight_LoadsContents is the marquee Phase 3 test. It
// drives a right-arrow press from stateBrowsing/colProjects through to
// the contentsLoadedMsg returned by loadProjectContentsCmd's fan-in,
// exercising the full chain: state transition → fan-out tea.Cmd →
// concurrent api.GetFolders + api.GetProjectItems → merge into a
// single contentsLoadedMsg.
func TestUpdate_NavigateRight_LoadsContents(t *testing.T) {
	handler := func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if req.AuthHeader != "Bearer test-token" {
			t.Errorf("AuthHeader = %q, want %q", req.AuthHeader, "Bearer test-token")
		}
		if got := req.Variables["projectId"]; got != "proj-1" {
			t.Errorf("projectId = %v, want \"proj-1\"", got)
		}
		if strings.Contains(req.Query, "foldersByProject") {
			return testutil.GraphQLResponse{Data: map[string]any{
				"foldersByProject": map[string]any{
					"pagination": map[string]any{"cursor": ""},
					"results": []map[string]any{
						{"id": "folder-1", "name": "Drawings"},
					},
				},
			}}
		}
		return testutil.GraphQLResponse{Data: map[string]any{
			"itemsByProject": map[string]any{
				"pagination": map[string]any{"cursor": ""},
				"results": []map[string]any{
					{"__typename": "DesignItem", "id": "design-1", "name": "Widget"},
				},
			},
		}}
	}
	srv := testutil.GraphQLServer(t, handler)
	t.Cleanup(api.SetGraphqlEndpointForTesting(srv.URL))

	m := Model{
		state:         stateBrowsing,
		width:         120,
		height:        40,
		activeCol:     colProjects,
		token:         "test-token",
		selectedHubID: "hub-1",
		cols: [numCols][]api.NavItem{
			{{ID: "proj-1", Name: "Project A", Kind: "project", AltID: "alt-1", IsContainer: true}},
			nil,
		},
		cursors:      [numCols]int{0, 0},
		spinner:      spinner.New(),
		styleCache:   &styleCache{},
		detailsCache: map[string]*api.ItemDetails{},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	um, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update did not return Model, got %T", updated)
	}
	if um.activeCol != colContents {
		t.Errorf("activeCol = %d, want colContents (%d)", um.activeCol, colContents)
	}
	if !um.loading[colContents] {
		t.Errorf("loading[colContents] = false, want true")
	}
	if um.selectedProjectAltID != "alt-1" {
		t.Errorf("selectedProjectAltID = %q, want %q", um.selectedProjectAltID, "alt-1")
	}
	if cmd == nil {
		t.Fatalf("cmd = nil, want load-contents cmd")
	}

	msg := cmd()
	loaded, ok := msg.(contentsLoadedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want contentsLoadedMsg", msg)
	}
	if got, want := len(loaded.items), 2; got != want {
		t.Fatalf("len(items) = %d, want %d (got items=%+v)", got, want, loaded.items)
	}
	// Folders come before items per loadProjectContentsCmd's append order.
	if loaded.items[0].Kind != "folder" || !loaded.items[0].IsContainer {
		t.Errorf("items[0] = %+v, want kind=folder & IsContainer=true", loaded.items[0])
	}
	if loaded.items[0].Name != "Drawings" {
		t.Errorf("items[0].Name = %q, want %q", loaded.items[0].Name, "Drawings")
	}
	if loaded.items[1].Kind != "design" {
		t.Errorf("items[1].Kind = %q, want \"design\"", loaded.items[1].Kind)
	}
	if loaded.items[1].Name != "Widget" {
		t.Errorf("items[1].Name = %q, want %q", loaded.items[1].Name, "Widget")
	}
}

// TestVerifySameHub covers the four code paths in verifySameHub:
//   - exact ID match (after NormalizeProjectID)
//   - case-insensitive name fallback when the ID can't be parsed
//   - no match → returns the "different hub" error naming the expected hub
//   - empty inputs short-circuit before the MCP call
func TestVerifySameHub(t *testing.T) {
	srv := testutil.NewMCPServer(t, testutil.MCPScenario{
		SessionID: "verify-sid",
		Tools: map[string]testutil.MCPHandler{
			"fusion_mcp_read": func(args map[string]any) testutil.MCPResponse {
				return testutil.MCPResponse{
					ContentText: `{"success": true, "projects": [` +
						`{"id": "20250213876602531", "name": "Buggy"},` +
						`{"id": "98765432101234567", "name": "Robot"}` +
						`]}`,
				}
			},
		},
	})
	client := &fusion.Client{Endpoint: srv.URL, HTTP: srv.Client()}

	encode := func(plain string) string {
		return "a." + base64.RawURLEncoding.EncodeToString([]byte(plain))
	}

	cases := []struct {
		name      string
		altID     string
		projName  string
		hubName   string
		wantErr   bool
		errSubstr []string
	}{
		{
			name:    "exact_id_match",
			altID:   encode("business:autodesk#20250213876602531"),
			hubName: "MyHub",
			wantErr: false,
		},
		{
			name:     "name_match_when_id_unparseable",
			altID:    "garbage",
			projName: "Robot",
			hubName:  "MyHub",
			wantErr:  false,
		},
		{
			name:      "no_match",
			altID:     encode("business:autodesk#9999"),
			projName:  "Nope",
			hubName:   "MyHub",
			wantErr:   true,
			errSubstr: []string{"different hub", "MyHub"},
		},
		{
			name:    "empty_skips_check",
			altID:   "",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := verifySameHub(ctx, client, tc.altID, tc.projName, tc.hubName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("verifySameHub: expected error, got nil")
				}
				for _, sub := range tc.errSubstr {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("verifySameHub: error %q missing substring %q", err.Error(), sub)
					}
				}
				return
			}
			if err != nil {
				t.Errorf("verifySameHub: unexpected error: %v", err)
			}
		})
	}
}

// TestRecoverFromError_AuthError_DeletesTokens locks down the auth-recovery
// behaviour: when the model is in stateError with an auth-flavored error,
// recoverFromError must remove the on-disk token file so the next
// checkTokensCmd run prompts for fresh login. Non-auth recovery paths skip
// the token deletion (covered implicitly by the explicit auth-error
// trigger here).
func TestRecoverFromError_AuthError_DeletesTokens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	td := &auth.TokenData{
		AccessToken:  "x",
		RefreshToken: "y",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := auth.SaveTokens(td); err != nil {
		t.Fatalf("SaveTokens: unexpected error: %v", err)
	}
	// Sanity-check that the file is on disk before we ask recoverFromError
	// to delete it — otherwise a missing-file false-positive would mask a
	// regression where the function silently does nothing.
	if got, err := auth.LoadTokens(); err != nil || got == nil {
		t.Fatalf("LoadTokens before recover: got=%v err=%v, want non-nil token", got, err)
	}

	m := Model{
		state:        stateError,
		err:          errors.New("401 unauthorized"),
		spinner:      spinner.New(),
		styleCache:   &styleCache{},
		detailsCache: map[string]*api.ItemDetails{},
	}
	if !isAuthError(m.err) {
		t.Fatalf("test setup: isAuthError(%v) = false; pick an auth-flavored message", m.err)
	}
	updated, cmd := m.recoverFromError()
	if updated.state != stateLoading {
		t.Errorf("state after recover = %d, want stateLoading (%d)", updated.state, stateLoading)
	}
	if updated.err != nil {
		t.Errorf("err after recover = %v, want nil", updated.err)
	}
	if cmd == nil {
		t.Errorf("cmd after recover = nil, want non-nil (spinner.Tick + checkTokensCmd batch)")
	}

	got, err := auth.LoadTokens()
	if err != nil {
		t.Fatalf("LoadTokens after recover: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("LoadTokens after recover: got %+v, want nil (file should be deleted)", got)
	}
}

// TestBreadcrumb_HitDetection exercises the pure buildBreadcrumb helper
// against a model with hub + project + a 2-deep folder stack, verifying
// the rendered text, the number of clickable hits, their kinds, and
// folder-stack indices.
func TestBreadcrumb_HitDetection(t *testing.T) {
	m := Model{
		width:                120,
		height:               40,
		selectedHubNameCache: "Hub",
		cols: [numCols][]api.NavItem{
			{{ID: "p1", Name: "Project", Kind: "project"}},
			{{ID: "f2", Name: "Subfolder", Kind: "folder", IsContainer: true}},
		},
		cursors: [numCols]int{0, 0},
		folderStack: []breadcrumbEntry{
			{id: "f1", name: "Outer"},
			{id: "f2", name: "Inner"},
		},
		activeCol:    colContents,
		spinner:      spinner.New(),
		styleCache:   &styleCache{},
		detailsCache: map[string]*api.ItemDetails{},
	}

	text, hits := m.buildBreadcrumb(breadcrumbXOffset())

	for _, want := range []string{"Hub", "Project", "Outer", "Inner"} {
		if !strings.Contains(text, want) {
			t.Errorf("breadcrumb text %q missing segment %q", text, want)
		}
	}
	if !strings.Contains(text, " › ") {
		t.Errorf("breadcrumb text %q missing separator %q", text, " › ")
	}

	if len(hits) != 4 {
		t.Fatalf("len(hits) = %d, want 4 (hub + project + 2 folders); hits=%+v", len(hits), hits)
	}

	wantKinds := []string{"hub", "project", "folder", "folder"}
	off := breadcrumbXOffset()
	for i, h := range hits {
		if h.kind != wantKinds[i] {
			t.Errorf("hits[%d].kind = %q, want %q", i, h.kind, wantKinds[i])
		}
		if h.xStart < off {
			t.Errorf("hits[%d].xStart = %d, want >= %d", i, h.xStart, off)
		}
		if h.xEnd <= h.xStart {
			t.Errorf("hits[%d]: xEnd %d <= xStart %d", i, h.xEnd, h.xStart)
		}
	}

	if hits[2].index != 0 {
		t.Errorf("hits[2].index = %d, want 0 (Outer folder)", hits[2].index)
	}
	if hits[3].index != 1 {
		t.Errorf("hits[3].index = %d, want 1 (Inner folder)", hits[3].index)
	}
}
