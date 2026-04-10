package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/schneik80/FusionDataCLI/api"
	"github.com/schneik80/FusionDataCLI/auth"
	"github.com/schneik80/FusionDataCLI/config"
	"github.com/schneik80/FusionDataCLI/fusion"
)

// ---------------------------------------------------------------------------
// App state
// ---------------------------------------------------------------------------

type appState int

const (
	stateSetupNeeded appState = iota // config file missing or incomplete
	stateLoading                     // checking saved tokens
	stateAuthNeeded                  // no token; prompt user to log in
	stateAuthWaiting                 // browser opened; waiting for callback
	stateBrowsing                    // main 2-column browser + details
	stateHubSelect                   // hub selection overlay
	stateAbout                       // about / license overlay
	stateDebug                       // debug log overlay
	stateError                       // unrecoverable error
)

// Column indices (hubs are now an overlay, not a column)
const (
	colProjects = 0
	colContents = 1
	numCols     = 2
)

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type (
	tokenReadyMsg      struct{ token string }
	hubsLoadedMsg      struct{ items []api.NavItem }
	projectsLoadedMsg  struct{ items []api.NavItem }
	contentsLoadedMsg  struct{ items []api.NavItem }
	detailsLoadedMsg   struct{ details *api.ItemDetails }
	errMsg            struct{ err error }
	openedBrowserMsg   struct{}
	// fusionActionMsg is the result of an asynchronous open/insert call
	// against the local Fusion MCP server. If err is non-nil, the status bar
	// shows the error; otherwise it shows the action string.
	fusionActionMsg struct {
		action string
		err    error
	}
)

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type breadcrumbEntry struct {
	id   string
	name string
}

// Model is the root bubbletea model for the apsnav browser.
type Model struct {
	state    appState
	width    int
	height   int
	err      error
	statusMsg string
	version   string

	// Auth
	token        string
	clientID     string
	clientSecret string

	// Hub data (shown as overlay, not a column)
	hubs       []api.NavItem
	hubCursor  int
	hubScroll  int
	hubLoading bool

	// Column data (projects=0, folders+items=1)
	cols    [numCols][]api.NavItem
	cursors [numCols]int
	loading [numCols]bool
	// scroll offsets for each column (for long lists)
	scrolls [numCols]int

	// Which column has keyboard focus
	activeCol int

	// Details panel (always visible)
	detailsLoading bool
	details        *api.ItemDetails
	detailsScroll  int

	// About / debug overlay scroll
	aboutScroll int
	debugScroll int

	// For column 2: when drilling into a subfolder, track the stack so we can go back.
	folderStack []breadcrumbEntry

	// IDs and URLs of the currently selected hub and project.
	selectedHubID         string
	selectedHubAltID      string
	selectedProjectAltID  string
	selectedProjectWebURL string // fusionWebUrl of the selected project, used as URL fallback

	spinner      spinner.Model
	mouseEnabled bool
}

// New creates the initial model. cfgErr may be non-nil when the config file is
// missing or invalid; the app will display a setup screen in that case.
func New(cfg *config.Config, cfgErr error, version string) Model {
	if os.Getenv("APSNAV_DEBUG") != "" {
		api.EnableDebug()
	}
	if cfg != nil && cfg.Region != "" {
		api.SetRegion(cfg.Region)
	}
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleLoading

	if cfgErr != nil {
		return Model{
			state:        stateSetupNeeded,
			err:          cfgErr,
			spinner:      sp,
			version:      version,
			mouseEnabled: true,
		}
	}

	return Model{
		state:        stateLoading,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		spinner:      sp,
		version:      version,
		mouseEnabled: true,
	}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func (m Model) Init() tea.Cmd {
	if m.state == stateSetupNeeded {
		return nil
	}
	return tea.Batch(m.spinner.Tick, checkTokensCmd(m.clientID, m.clientSecret))
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func checkTokensCmd(clientID, clientSecret string) tea.Cmd {
	return func() tea.Msg {
		td, err := auth.LoadTokens()
		if err != nil {
			return errMsg{err}
		}
		if td == nil {
			return tokenReadyMsg{token: ""}
		}
		if td.Valid() {
			return tokenReadyMsg{token: td.AccessToken}
		}
		if td.RefreshToken != "" {
			refreshed, err := auth.Refresh(context.Background(), clientID, clientSecret, td.RefreshToken)
			if err != nil {
				// Refresh failed — prompt fresh login.
				return tokenReadyMsg{token: ""}
			}
			return tokenReadyMsg{token: refreshed.AccessToken}
		}
		return tokenReadyMsg{token: ""}
	}
}

func loginCmd(clientID, clientSecret string) tea.Cmd {
	return func() tea.Msg {
		td, err := auth.Login(context.Background(), clientID, clientSecret)
		if err != nil {
			return errMsg{err}
		}
		return tokenReadyMsg{token: td.AccessToken}
	}
}

func loadHubsCmd(token string) tea.Cmd {
	return func() tea.Msg {
		items, err := api.GetHubs(context.Background(), token)
		if err != nil {
			return errMsg{err}
		}
		return hubsLoadedMsg{items}
	}
}

func loadProjectsCmd(token, hubID string) tea.Cmd {
	return func() tea.Msg {
		items, err := api.GetProjects(context.Background(), token, hubID)
		if err != nil {
			return errMsg{err}
		}
		return projectsLoadedMsg{items}
	}
}

// loadProjectContentsCmd loads the root contents of a project.
// It fetches both top-level folders (foldersByProject) and project-level items
// (itemsByProject) and merges them — folders first, then items.
func loadProjectContentsCmd(token, projectID string) tea.Cmd {
	return func() tea.Msg {
		folders, err := api.GetFolders(context.Background(), token, projectID)
		if err != nil {
			return errMsg{err}
		}
		items, err := api.GetProjectItems(context.Background(), token, projectID)
		if err != nil {
			return errMsg{err}
		}
		combined := append(folders, items...)
		return contentsLoadedMsg{combined}
	}
}

func loadItemsCmd(token, hubID, folderID string) tea.Cmd {
	return func() tea.Msg {
		items, err := api.GetItems(context.Background(), token, hubID, folderID)
		if err != nil {
			return errMsg{err}
		}
		return contentsLoadedMsg{items}
	}
}

func loadDetailsCmd(token, hubID, itemID string) tea.Cmd {
	return func() tea.Msg {
		d, err := api.GetItemDetails(context.Background(), token, hubID, itemID)
		if err != nil {
			return errMsg{err}
		}
		return detailsLoadedMsg{d}
	}
}

func openURLCmd(u string) tea.Cmd {
	return func() tea.Msg {
		_ = auth.OpenBrowser(u)
		return openedBrowserMsg{}
	}
}

// openInFusionCmd asks the running Fusion desktop app (via its local MCP
// server) to open the document identified by the lineage URN. Before sending
// the open call, it verifies that Fusion's active hub contains the CLI's
// currently-selected project; if not, it returns a message instructing the
// user to switch hubs in Fusion and performs no action.
func openInFusionCmd(fileID, expectedProjectAltID, expectedProjectName, expectedHubName string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		client := fusion.NewClient()
		if err := verifySameHub(ctx, client, expectedProjectAltID, expectedProjectName, expectedHubName); err != nil {
			return fusionActionMsg{err: err}
		}
		if err := client.OpenDocument(ctx, fileID); err != nil {
			return fusionActionMsg{err: err}
		}
		return fusionActionMsg{action: "Opened in Fusion"}
	}
}

// insertInFusionCmd asks the running Fusion desktop app (via its local MCP
// server) to insert the document identified by the lineage URN as an
// occurrence in the active design. Blocked if Fusion is on a different hub
// (see openInFusionCmd).
func insertInFusionCmd(fileID, expectedProjectAltID, expectedProjectName, expectedHubName string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		client := fusion.NewClient()
		if err := verifySameHub(ctx, client, expectedProjectAltID, expectedProjectName, expectedHubName); err != nil {
			return fusionActionMsg{err: err}
		}
		if err := client.InsertDocument(ctx, fileID); err != nil {
			return fusionActionMsg{err: err}
		}
		return fusionActionMsg{action: "Inserted in Fusion"}
	}
}

// verifySameHub returns nil when Fusion's active hub contains the CLI's
// currently-selected project. Otherwise it returns an error whose message
// names the expected hub so the status bar can tell the user to switch
// hubs in Fusion.
//
// The CLI stores a project's APS Data Management API ID (e.g.
// "a.YnVzaW5lc3M6YXV0b2Rlc2s4MDgzIzIwMjUwMjEzODc2NjAyNTMx") but Fusion's
// local MCP server reports the raw internal ID (e.g. "20250213876602531"),
// so we convert with fusion.NormalizeProjectID before comparing.
//
// An empty expectedProjectAltID (e.g. if the CLI hasn't drilled into a
// project yet) skips the check. If conversion fails, we fall back to a
// case-insensitive match on the project name so we don't spuriously block
// on an unexpected ID format.
func verifySameHub(ctx context.Context, client *fusion.Client, expectedProjectAltID, expectedProjectName, expectedHubName string) error {
	if expectedProjectAltID == "" && expectedProjectName == "" {
		return nil
	}
	projects, err := client.ActiveHubProjects(ctx)
	if err != nil {
		return fmt.Errorf("could not verify Fusion hub: %w", err)
	}
	wantID := fusion.NormalizeProjectID(expectedProjectAltID)
	wantName := strings.TrimSpace(strings.ToLower(expectedProjectName))
	for _, p := range projects {
		if wantID != "" && p.ID == wantID {
			return nil
		}
		if wantID == "" && wantName != "" && strings.TrimSpace(strings.ToLower(p.Name)) == wantName {
			return nil
		}
	}
	hubLabel := expectedHubName
	if hubLabel == "" {
		hubLabel = "the selected hub"
	}
	return fmt.Errorf("Fusion is on a different hub — switch Fusion to %q and retry", hubLabel)
}

func openBrowserCmd(item api.NavItem, hubAltID, projectAltID string) tea.Cmd {
	return func() tea.Msg {
		u := itemWebURL(item, hubAltID, projectAltID)
		_ = auth.OpenBrowser(u)
		return openedBrowserMsg{}
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tokenReadyMsg:
		if msg.token == "" {
			m.state = stateAuthNeeded
			return m, nil
		}
		m.token = msg.token
		m.state = stateLoading
		m.hubLoading = true
		return m, loadHubsCmd(m.token)

	case hubsLoadedMsg:
		m.hubLoading = false
		m.hubs = msg.items
		m.hubCursor = 0
		m.hubScroll = 0
		// Auto-select if only one hub, otherwise show hub overlay
		if len(msg.items) == 1 {
			m.state = stateBrowsing
			m.activeCol = colProjects
			m.selectedHubID = msg.items[0].ID
			m.selectedHubAltID = msg.items[0].AltID
			m.loading[colProjects] = true
			return m, loadProjectsCmd(m.token, msg.items[0].ID)
		}
		m.state = stateHubSelect
		m.activeCol = colProjects
		return m, nil

	case projectsLoadedMsg:
		m.loading[colProjects] = false
		m.cols[colProjects] = msg.items
		m.cursors[colProjects] = 0
		m.scrolls[colProjects] = 0
		// Clear stale contents
		m.cols[colContents] = nil
		m.folderStack = nil
		m.selectedProjectAltID = ""
		return m, nil

	case contentsLoadedMsg:
		m.loading[colContents] = false
		m.cols[colContents] = msg.items
		m.cursors[colContents] = 0
		m.scrolls[colContents] = 0
		// Auto-load details for the first item
		if len(msg.items) > 0 && !msg.items[0].IsContainer && m.selectedHubID != "" {
			m.detailsLoading = true
			m.details = nil
			m.detailsScroll = 0
			return m, loadDetailsCmd(m.token, m.selectedHubID, msg.items[0].ID)
		}
		m.details = nil
		return m, nil

	case detailsLoadedMsg:
		m.detailsLoading = false
		m.details = msg.details
		m.detailsScroll = 0
		return m, nil

	case openedBrowserMsg:
		m.statusMsg = "Opened in browser"
		return m, nil

	case fusionActionMsg:
		if msg.err != nil {
			m.statusMsg = "Fusion: " + msg.err.Error()
		} else {
			m.statusMsg = msg.action
		}
		return m, nil

	case errMsg:
		m.err = msg.err
		m.state = stateError
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, keys.About):
		if m.state == stateAbout {
			m.state = stateBrowsing
		} else if m.state == stateBrowsing || m.state == stateAuthNeeded {
			m.aboutScroll = 0
			m.state = stateAbout
		}
		return m, nil

	case m.state == stateAbout && key.Matches(msg, keys.Up):
		if m.aboutScroll > 0 {
			m.aboutScroll--
		}
		return m, nil

	case m.state == stateAbout && key.Matches(msg, keys.Down):
		m.aboutScroll++
		return m, nil

	case m.state == stateAbout:
		// any other key closes about
		m.state = stateBrowsing
		return m, nil

	case key.Matches(msg, keys.Hub):
		if m.state == stateHubSelect {
			m.state = stateBrowsing
		} else if m.state == stateBrowsing {
			m.hubScroll = 0
			m.state = stateHubSelect
		}
		return m, nil

	case m.state == stateHubSelect && key.Matches(msg, keys.Up):
		if len(m.hubs) > 0 && m.hubCursor > 0 {
			m.hubCursor--
			m.adjustHubScroll()
		}
		return m, nil

	case m.state == stateHubSelect && key.Matches(msg, keys.Down):
		if len(m.hubs) > 0 && m.hubCursor < len(m.hubs)-1 {
			m.hubCursor++
			m.adjustHubScroll()
		}
		return m, nil

	case m.state == stateHubSelect && (key.Matches(msg, keys.Enter) || key.Matches(msg, keys.Right)):
		return m.selectHub()

	case m.state == stateHubSelect && key.Matches(msg, keys.Refresh):
		m.hubs = nil
		m.hubLoading = true
		return m, loadHubsCmd(m.token)

	case m.state == stateHubSelect:
		// ignore other keys in hub overlay
		return m, nil

	case key.Matches(msg, keys.Debug):
		if m.state == stateDebug {
			m.state = stateBrowsing
		} else if m.state == stateBrowsing {
			m.debugScroll = 0
			m.state = stateDebug
		}
		return m, nil

	case m.state == stateDebug && key.Matches(msg, keys.Up):
		if m.debugScroll > 0 {
			m.debugScroll--
		}
		return m, nil

	case m.state == stateDebug && key.Matches(msg, keys.Down):
		m.debugScroll++
		return m, nil

	case m.state == stateAuthNeeded && key.Matches(msg, keys.Enter):
		m.state = stateAuthWaiting
		return m, tea.Batch(m.spinner.Tick, loginCmd(m.clientID, m.clientSecret))

	case m.state == stateError && (key.Matches(msg, keys.Refresh) || key.Matches(msg, keys.Enter)):
		return m.recoverFromError()

	case m.state != stateBrowsing:
		return m, nil

	case key.Matches(msg, keys.Up):
		m.moveCursor(-1)
		m.detailsScroll = 0
		return m, m.maybeLoadDetails()

	case key.Matches(msg, keys.Down):
		m.moveCursor(1)
		m.detailsScroll = 0
		return m, m.maybeLoadDetails()

	case key.Matches(msg, keys.Left):
		return m.navigateLeft()

	case key.Matches(msg, keys.Right), key.Matches(msg, keys.Enter):
		return m.navigateRight()

	case key.Matches(msg, keys.Open):
		return m.openInBrowser()

	case key.Matches(msg, keys.OpenDesktop):
		return m.openInDesktop()

	case key.Matches(msg, keys.Insert):
		return m.insertInDesktop()

	case key.Matches(msg, keys.Refresh):
		return m.refresh()

	case key.Matches(msg, keys.Theme):
		name := cycleTheme()
		m.spinner.Style = styleLoading
		m.statusMsg = "Theme: " + name
		return m, nil

	case key.Matches(msg, keys.Mouse):
		m.mouseEnabled = !m.mouseEnabled
		if m.mouseEnabled {
			m.statusMsg = "Mouse: on"
			return m, tea.EnableMouseCellMotion
		}
		m.statusMsg = "Mouse: off"
		return m, tea.DisableMouse
	}

	return m, nil
}

// handleMouse processes mouse events when mouse support is enabled.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.mouseEnabled {
		return m, nil
	}

	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return m.mouseScroll(-3)
	case tea.MouseButtonWheelDown:
		return m.mouseScroll(3)
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		return m.mouseClick(msg.X, msg.Y)
	}
	return m, nil
}

// mouseScroll handles scroll wheel events based on current state.
func (m Model) mouseScroll(delta int) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateBrowsing:
		m.moveCursor(delta)
		m.detailsScroll = 0
		return m, m.maybeLoadDetails()
	case stateHubSelect:
		for range abs(delta) {
			if delta < 0 && m.hubCursor > 0 {
				m.hubCursor--
			} else if delta > 0 && m.hubCursor < len(m.hubs)-1 {
				m.hubCursor++
			}
		}
		m.adjustHubScroll()
		return m, nil
	case stateAbout:
		m.aboutScroll += delta
		if m.aboutScroll < 0 {
			m.aboutScroll = 0
		}
		return m, nil
	case stateDebug:
		m.debugScroll += delta
		if m.debugScroll < 0 {
			m.debugScroll = 0
		}
		return m, nil
	}
	return m, nil
}

// mouseClick handles left-click events in the browsing state.
func (m Model) mouseClick(x, y int) (tea.Model, tea.Cmd) {
	if m.state == stateHubSelect {
		return m.mouseClickHub(y)
	}
	if m.state != stateBrowsing {
		return m, nil
	}

	// Breadcrumb hit test: the header is on row 0. If the click lands on a
	// clickable segment, jump to that level instead of falling through to
	// the column-click logic.
	if y == 0 {
		if _, hits := m.buildBreadcrumb(breadcrumbXOffset()); len(hits) > 0 {
			for _, h := range hits {
				if x >= h.xStart && x < h.xEnd {
					return m.clickBreadcrumb(h)
				}
			}
		}
		return m, nil
	}

	// Determine column layout (mirrors viewBrowser).
	detailsWidth := (m.width * 35) / 100
	navWidth := m.width - detailsWidth - 2
	colWidth := (navWidth - 4) / numCols
	if colWidth < 10 {
		colWidth = 10
	}

	// Each column is rendered with style.Width(colWidth) which is the outer
	// width (includes border + padding). Columns are placed side-by-side by
	// lipgloss.JoinHorizontal.
	col := -1
	for i := 0; i < numCols; i++ {
		colStart := i * colWidth
		colEnd := colStart + colWidth
		if x >= colStart && x < colEnd {
			col = i
			break
		}
	}

	// Y layout: header(1) + border-top(1) + title-row(1) + padding = first item at y=4.
	const firstItemY = 4
	row := y - firstItemY

	if col < 0 {
		return m, nil
	}

	row += m.scrolls[col]
	items := m.cols[col]
	if row < 0 || row >= len(items) {
		return m, nil
	}

	if col != m.activeCol {
		m.activeCol = col
	}

	m.cursors[col] = row
	m.adjustScroll(col)
	m.detailsScroll = 0

	// For projects column or folders in contents, navigate into the item.
	// For documents in contents, just load details.
	item := m.cols[col][row]
	if col == colProjects || item.IsContainer {
		return m.navigateRight()
	}
	return m, m.maybeLoadDetails()
}

// mouseClickHub handles clicking on a hub in the hub selection overlay.
func (m Model) mouseClickHub(y int) (tea.Model, tea.Cmd) {
	// The hub overlay is centered; rows start after the overlay border + title.
	// Approximate: the overlay header takes ~3 rows from top of overlay.
	// Since exact positioning depends on centering, use a simpler approach:
	// map y to hub index relative to scroll.
	const overlayHeaderRows = 4 // border + title + blank + list start
	centerY := (m.height - len(m.hubs) - overlayHeaderRows) / 2
	if centerY < 0 {
		centerY = 0
	}
	idx := y - centerY - overlayHeaderRows + m.hubScroll
	if idx < 0 || idx >= len(m.hubs) {
		return m, nil
	}
	m.hubCursor = idx
	return m.selectHub()
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// moveCursor moves the cursor in the active column and adjusts scroll.
func (m *Model) moveCursor(delta int) {
	col := m.activeCol
	items := m.cols[col]
	if len(items) == 0 {
		return
	}
	m.cursors[col] = clamp(m.cursors[col]+delta, 0, len(items)-1)
	m.adjustScroll(col)
}

// adjustScroll keeps the cursor visible in the column.
func (m *Model) adjustScroll(col int) {
	visible := m.visibleRows()
	c := m.cursors[col]
	s := m.scrolls[col]
	if c < s {
		m.scrolls[col] = c
	} else if c >= s+visible {
		m.scrolls[col] = c - visible + 1
	}
}

// crumbHit describes one clickable segment of the breadcrumb bar.
// xStart is inclusive, xEnd is exclusive, both measured in terminal columns
// from the leftmost column of the window.
type crumbHit struct {
	xStart, xEnd int
	kind         string // "hub" | "project" | "folder"
	index        int    // folder stack index for "folder", unused otherwise
}

// breadcrumbXOffset returns the absolute x column at which the breadcrumb
// segments begin inside the header row. It accounts for the left padding of
// styleHeader plus the fixed "FusionDataCLI  " prefix.
func breadcrumbXOffset() int {
	// styleHeader.Padding(0, 1) contributes 1 leading column.
	return 1 + lipgloss.Width("FusionDataCLI  ")
}

// buildBreadcrumb returns the plain-text breadcrumb string (with " › "
// separators) and the list of clickable segment regions. xOffset is the
// absolute x column of the first rune of the breadcrumb text.
//
// The terminal document (a non-container item on colContents) is included in
// the text but is NOT clickable — clicking the currently shown document does
// nothing useful beyond what's already on screen.
func (m Model) buildBreadcrumb(xOffset int) (string, []crumbHit) {
	const sep = " › "
	sepW := lipgloss.Width(sep)

	var sb strings.Builder
	var hits []crumbHit
	x := xOffset
	first := true

	addSeg := func(text, kind string, idx int, clickable bool) {
		if text == "" {
			return
		}
		if !first {
			sb.WriteString(sep)
			x += sepW
		}
		first = false
		w := lipgloss.Width(text)
		if clickable {
			hits = append(hits, crumbHit{xStart: x, xEnd: x + w, kind: kind, index: idx})
		}
		sb.WriteString(text)
		x += w
	}

	for _, h := range m.hubs {
		if h.ID == m.selectedHubID {
			addSeg(h.Name, "hub", 0, true)
			break
		}
	}
	if proj := m.selectedItem(colProjects); proj != nil {
		addSeg(proj.Name, "project", 0, true)
	}
	for i, f := range m.folderStack {
		addSeg(f.name, "folder", i, true)
	}
	if item := m.selectedItem(colContents); item != nil && !item.IsContainer {
		addSeg(item.Name, "document", 0, false)
	}
	return sb.String(), hits
}

// clickBreadcrumb navigates to the level described by a breadcrumb hit.
//
//   - hub:     opens the hub-select overlay.
//   - project: clears the folder stack and reloads the project's root.
//   - folder:  truncates the folder stack to the clicked depth and reloads
//     the contents of that folder.
func (m Model) clickBreadcrumb(h crumbHit) (Model, tea.Cmd) {
	switch h.kind {
	case "hub":
		m.hubScroll = 0
		m.state = stateHubSelect
		return m, nil

	case "project":
		proj := m.selectedItem(colProjects)
		if proj == nil {
			return m, nil
		}
		m.selectedProjectAltID = proj.AltID
		m.selectedProjectWebURL = proj.WebURL
		m.activeCol = colContents
		m.folderStack = nil
		m.cols[colContents] = nil
		m.loading[colContents] = true
		m.details = nil
		m.detailsScroll = 0
		return m, loadProjectContentsCmd(m.token, proj.ID)

	case "folder":
		if h.index < 0 || h.index >= len(m.folderStack) {
			return m, nil
		}
		// Truncate to include only up to and including the clicked folder.
		target := m.folderStack[h.index]
		m.folderStack = m.folderStack[:h.index+1]
		m.activeCol = colContents
		m.cols[colContents] = nil
		m.loading[colContents] = true
		m.details = nil
		m.detailsScroll = 0
		return m, loadItemsCmd(m.token, m.selectedHubID, target.id)
	}
	return m, nil
}

// navigateLeft moves focus left or goes up a folder level, returning a reload
// command when the folder stack is popped.
func (m Model) navigateLeft() (Model, tea.Cmd) {
	switch m.activeCol {
	case colContents:
		if len(m.folderStack) > 0 {
			// Pop folder stack and reload the parent's contents.
			m.folderStack = m.folderStack[:len(m.folderStack)-1]
			m.cols[colContents] = nil
			m.loading[colContents] = true
			if len(m.folderStack) > 0 {
				// Reload the folder that's now on top of the stack.
				return m, loadItemsCmd(m.token, m.selectedHubID, m.folderStack[len(m.folderStack)-1].id)
			}
			// Back to project root folders.
			proj := m.selectedItem(colProjects)
			if proj != nil {
				return m, loadProjectContentsCmd(m.token, proj.ID)
			}
			m.loading[colContents] = false
		} else {
			m.activeCol = colProjects
		}
	case colProjects:
		// Already at leftmost column.
	}
	return m, nil
}

// navigateRight moves focus right, loading the next level.
func (m Model) navigateRight() (Model, tea.Cmd) {
	switch m.activeCol {
	case colProjects:
		item := m.selectedItem(colProjects)
		if item == nil {
			return m, nil
		}
		m.selectedProjectAltID = item.AltID
		m.selectedProjectWebURL = item.WebURL
		m.activeCol = colContents
		m.cols[colContents] = nil
		m.folderStack = nil
		m.loading[colContents] = true
		return m, loadProjectContentsCmd(m.token, item.ID)

	case colContents:
		item := m.selectedItem(colContents)
		if item == nil {
			return m, nil
		}
		if item.IsContainer {
			// Drill into sub-folder.
			m.folderStack = append(m.folderStack, breadcrumbEntry{id: item.ID, name: item.Name})
			m.cols[colContents] = nil
			m.loading[colContents] = true
			return m, loadItemsCmd(m.token, m.selectedHubID, item.ID)
		}
		// Non-container: details already visible, no-op for right arrow.
	}
	return m, nil
}

// maybeLoadDetails loads details for the current item if it's a document.
func (m *Model) maybeLoadDetails() tea.Cmd {
	item := m.selectedItem(m.activeCol)
	if item == nil || item.IsContainer {
		m.details = nil
		m.detailsLoading = false
		return nil
	}
	m.detailsLoading = true
	return loadDetailsCmd(m.token, m.selectedHubID, item.ID)
}

// openInBrowser opens the selected item in the default browser.
// Priority: details panel URL > item's own WebURL > project WebURL > constructed fallback.
func (m Model) openInBrowser() (Model, tea.Cmd) {
	item := m.selectedItem(m.activeCol)
	if item == nil {
		return m, nil
	}
	// For design/drawing items, prefer the permalink from the loaded details panel.
	if !item.IsContainer && m.details != nil && m.details.FusionWebURL != "" {
		m.statusMsg = "Opening…"
		return m, openURLCmd(m.details.FusionWebURL)
	}
	// For items without a details URL, use the project's fusionWebUrl as a fallback.
	if !item.IsContainer && m.selectedProjectWebURL != "" {
		m.statusMsg = "Opening…"
		return m, openURLCmd(m.selectedProjectWebURL)
	}
	m.statusMsg = "Opening…"
	return m, openBrowserCmd(*item, m.selectedHubAltID, m.selectedProjectAltID)
}

// openInDesktop asks the running Fusion desktop client to open the selected
// document via its local MCP server. Requires Fusion to be running.
// Blocks the call if Fusion's active hub differs from the CLI's selected hub.
func (m Model) openInDesktop() (Model, tea.Cmd) {
	item := m.selectedItem(m.activeCol)
	if item == nil || item.IsContainer {
		return m, nil
	}
	projName := ""
	if proj := m.selectedItem(colProjects); proj != nil {
		projName = proj.Name
	}
	m.statusMsg = "Opening in Fusion…"
	return m, openInFusionCmd(item.ID, m.selectedProjectAltID, projName, m.selectedHubName())
}

// insertInDesktop asks the running Fusion desktop client to insert the
// selected document as an occurrence in the currently active design, via its
// local MCP server. Requires Fusion to be running with an active design.
// Blocks the call if Fusion's active hub differs from the CLI's selected hub.
func (m Model) insertInDesktop() (Model, tea.Cmd) {
	item := m.selectedItem(m.activeCol)
	if item == nil || item.IsContainer {
		return m, nil
	}
	projName := ""
	if proj := m.selectedItem(colProjects); proj != nil {
		projName = proj.Name
	}
	m.statusMsg = "Inserting in Fusion…"
	return m, insertInFusionCmd(item.ID, m.selectedProjectAltID, projName, m.selectedHubName())
}

// selectedHubName returns the display name of the currently-selected hub,
// or an empty string if nothing is selected. Used to build helpful error
// messages when Fusion is on a different hub than the CLI.
func (m Model) selectedHubName() string {
	for _, h := range m.hubs {
		if h.ID == m.selectedHubID {
			return h.Name
		}
	}
	return ""
}

// openInViewer opens the web viewer for the currently selected design item.
// refresh reloads the data for the active column.
func (m Model) refresh() (Model, tea.Cmd) {
	switch m.activeCol {
	case colProjects:
		if m.selectedHubID == "" {
			return m, nil
		}
		m.cols[colProjects] = nil
		m.loading[colProjects] = true
		return m, loadProjectsCmd(m.token, m.selectedHubID)

	case colContents:
		if len(m.folderStack) > 0 {
			// Reload current folder
			entry := m.folderStack[len(m.folderStack)-1]
			m.cols[colContents] = nil
			m.loading[colContents] = true
			return m, loadItemsCmd(m.token, m.selectedHubID, entry.id)
		}
		proj := m.selectedItem(colProjects)
		if proj == nil {
			return m, nil
		}
		m.cols[colContents] = nil
		m.loading[colContents] = true
		return m, loadProjectContentsCmd(m.token, proj.ID)
	}
	return m, nil
}

// selectHub confirms the hub selection from the overlay and loads projects.
func (m Model) selectHub() (Model, tea.Cmd) {
	if len(m.hubs) == 0 {
		return m, nil
	}
	hub := m.hubs[m.hubCursor]
	m.selectedHubID = hub.ID
	m.selectedHubAltID = hub.AltID
	m.state = stateBrowsing
	m.activeCol = colProjects
	m.cols[colProjects] = nil
	m.cols[colContents] = nil
	m.loading[colProjects] = true
	m.details = nil
	m.folderStack = nil
	return m, loadProjectsCmd(m.token, hub.ID)
}

// adjustHubScroll keeps the hub cursor visible in the overlay.
func (m *Model) adjustHubScroll() {
	visible := m.visibleRows()
	if m.hubCursor < m.hubScroll {
		m.hubScroll = m.hubCursor
	} else if m.hubCursor >= m.hubScroll+visible {
		m.hubScroll = m.hubCursor - visible + 1
	}
}

// selectedItem returns a pointer to the item at the cursor in a given column, or nil.
func (m *Model) selectedItem(col int) *api.NavItem {
	items := m.cols[col]
	if len(items) == 0 {
		return nil
	}
	idx := clamp(m.cursors[col], 0, len(items)-1)
	return &items[idx]
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	if m.width == 0 {
		return "Loading…"
	}

	switch m.state {
	case stateSetupNeeded:
		return m.viewSetupNeeded()
	case stateLoading:
		return m.viewLoading("Starting up…")
	case stateAuthNeeded:
		return m.viewAuthNeeded()
	case stateAuthWaiting:
		return m.viewLoading("Waiting for browser authentication…")
	case stateHubSelect:
		return m.viewHubSelect()
	case stateAbout:
		return m.viewAbout()
	case stateDebug:
		return m.viewDebug()
	case stateError:
		return m.viewError()
	}

	return m.viewBrowser()
}

func (m Model) viewLoading(msg string) string {
	content := fmt.Sprintf("\n\n  %s %s\n", m.spinner.View(), styleStatus.Render(msg))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

func (m Model) viewAuthNeeded() string {
	title := styleHeader.Render("FusionDataCLI")
	body := lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		styleStatus.Render("  Sign in with your Autodesk account to continue."),
		"",
		styleItemNormal.Render("  Press [Enter] to open your browser and log in."),
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, body)
}

func (m Model) viewHubSelect() string {
	header := styleHeader.Render("FusionDataCLI — Select Hub") +
		styleStatus.Render("  [↑↓/jk] move  [Enter] select  [r] refresh  [h] close")

	if m.hubLoading {
		body := fmt.Sprintf("\n  %s %s\n", m.spinner.View(), styleLoading.Render("Loading hubs…"))
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}

	if len(m.hubs) == 0 {
		body := styleItemDim.Render("\n  No hubs found.\n")
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}

	// Current selection indicator
	current := ""
	if m.selectedHubID != "" {
		for _, h := range m.hubs {
			if h.ID == m.selectedHubID {
				current = styleItemDim.Render("  Current: " + h.Name)
				break
			}
		}
	}

	visibleH := m.height - 5
	if visibleH < 1 {
		visibleH = 1
	}
	scroll := clamp(m.hubScroll, 0, max(0, len(m.hubs)-visibleH))
	end := min(scroll+visibleH, len(m.hubs))

	innerWidth := m.width - 8
	if innerWidth < 20 {
		innerWidth = 20
	}

	var sb strings.Builder
	if current != "" {
		sb.WriteString(current)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("\n")
	}
	for i := scroll; i < end; i++ {
		hub := m.hubs[i]
		icon := kindIcon(hub.Kind)
		label := truncate(icon+hub.Name, innerWidth)
		if i == m.hubCursor {
			sb.WriteString(styleItemSelected.Width(innerWidth).Render(label))
		} else {
			sb.WriteString(styleContainerItem.Width(innerWidth).Render(label))
		}
		if i < end-1 {
			sb.WriteString("\n")
		}
	}
	if scroll > 0 {
		sb.WriteString("\n" + styleItemDim.Render("  ↑ more"))
	}
	if end < len(m.hubs) {
		sb.WriteString("\n" + styleItemDim.Render("  ↓ more"))
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, sb.String())
}

func (m Model) viewSetupNeeded() string {
	cfgPath := config.Path()
	title := styleHeader.Render("FusionDataCLI — developer setup")
	body := lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		styleError.Render("  No APS client_id found."),
		styleItemDim.Render("  This binary was built without an embedded client_id."),
		"",
		styleItemNormal.Render("  Option 1 — build with embedded client_id:"),
		styleItemNormal.Render(`    go build -ldflags \`),
		styleItemNormal.Render(`      "-X github.com/schneik80/FusionDataCLI/config.DefaultClientID=<id>" .`),
		"",
		styleItemNormal.Render("  Option 2 — environment variable:"),
		styleItemNormal.Render("    APS_CLIENT_ID=<id> apsnav"),
		"",
		styleItemNormal.Render("  Option 3 — config file at:"),
		styleItemNormal.Render("    "+cfgPath),
		styleItemNormal.Render(`    { "client_id": "<id>" }`),
		styleItemNormal.Render(`    { "client_id": "<id>", "region": "EMEA" }  ← non-US hubs`),
		"",
		styleItemDim.Render("  Register a public APS app at: https://aps.autodesk.com/myapps"),
		styleItemDim.Render("  Redirect URI: http://localhost:7879/callback  Scopes: data:read"),
		styleItemDim.Render("  No client_secret needed for public clients."),
		"",
		styleItemDim.Render("  Press [q] to quit."),
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Center, body)
}

func (m Model) viewDebug() string {
	header := styleHeader.Render("FusionDataCLI — debug log") +
		styleStatus.Render("  [?] close  [↑↓/jk] scroll")
	if !api.DebugEnabled() {
		body := styleItemDim.Render("\n  Debug mode is off. Re-launch with APSNAV_DEBUG=1 to enable logging.\n")
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}

	lines := api.DebugLines()
	if len(lines) == 0 {
		body := styleItemDim.Render("\n  No log entries yet.\n")
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}

	// Visible area: full height minus header row
	visibleH := m.height - 3
	if visibleH < 1 {
		visibleH = 1
	}
	scroll := clamp(m.debugScroll, 0, max(0, len(lines)-visibleH))
	m.debugScroll = scroll

	end := min(scroll+visibleH, len(lines))
	var sb strings.Builder
	for _, l := range lines[scroll:end] {
		sb.WriteString(styleItemNormal.Render(l))
		sb.WriteString("\n")
	}
	footer := styleItemDim.Render(fmt.Sprintf("  lines %d–%d of %d", scroll+1, end, len(lines)))

	return lipgloss.JoinVertical(lipgloss.Left, header, sb.String(), footer)
}

func (m Model) viewAbout() string {
	ver := m.version
	if ver == "" {
		ver = "dev"
	}

	// Build scrollable content lines
	lines := []string{
		styleHeader.Render("FusionDataCLI  v" + ver),
		"",
		styleItemNormal.Render("  A terminal browser for Autodesk Platform Services"),
		styleItemNormal.Render("  Manufacturing Data Model — navigate Fusion hubs,"),
		styleItemNormal.Render("  projects, folders, and designs from the command line."),
		"",
		styleItemDim.Render("  https://github.com/schneik80/FusionDataCLI"),
		"",
		styleColumnTitle.MarginBottom(0).Render("Copyright:"),
		styleItemNormal.Render("  © 2025 Kevin Schneider"),
		"",
		styleColumnTitle.MarginBottom(0).Render("License:"),
		styleItemNormal.Render("  MIT License"),
		"",
		styleItemNormal.Render("  Permission is hereby granted, free of charge, to any person"),
		styleItemNormal.Render("  obtaining a copy of this software and associated documentation"),
		styleItemNormal.Render("  files (the \"Software\"), to deal in the Software without"),
		styleItemNormal.Render("  restriction, including without limitation the rights to use,"),
		styleItemNormal.Render("  copy, modify, merge, publish, distribute, sublicense, and/or"),
		styleItemNormal.Render("  sell copies of the Software, and to permit persons to whom the"),
		styleItemNormal.Render("  Software is furnished to do so, subject to the following"),
		styleItemNormal.Render("  conditions:"),
		"",
		styleItemNormal.Render("  The above copyright notice and this permission notice shall be"),
		styleItemNormal.Render("  included in all copies or substantial portions of the Software."),
		"",
		styleItemNormal.Render("  THE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND,"),
		styleItemNormal.Render("  EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES"),
		styleItemNormal.Render("  OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND"),
		styleItemNormal.Render("  NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT"),
		styleItemNormal.Render("  HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,"),
		styleItemNormal.Render("  WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING"),
		styleItemNormal.Render("  FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR"),
		styleItemNormal.Render("  OTHER DEALINGS IN THE SOFTWARE."),
		"",
		styleColumnTitle.MarginBottom(0).Render("Open Source:"),
		styleItemNormal.Render("  This application uses the following open source libraries:"),
		"",
		styleItemNormal.Render("  Charm.sh bubbletea"),
		styleItemDim.Render("    TUI framework — github.com/charmbracelet/bubbletea"),
		styleItemDim.Render("    MIT License — © Charmbracelet, Inc."),
		"",
		styleItemNormal.Render("  Charm.sh bubbles"),
		styleItemDim.Render("    TUI components — github.com/charmbracelet/bubbles"),
		styleItemDim.Render("    MIT License — © Charmbracelet, Inc."),
		"",
		styleItemNormal.Render("  Charm.sh lipgloss"),
		styleItemDim.Render("    Terminal styling — github.com/charmbracelet/lipgloss"),
		styleItemDim.Render("    MIT License — © Charmbracelet, Inc."),
		"",
		styleColumnTitle.MarginBottom(0).Render("Autodesk Platform Services:"),
		styleItemNormal.Render("  Powered by the APS Manufacturing Data Model API."),
		styleItemDim.Render("  Autodesk, Fusion, and related marks are trademarks of"),
		styleItemDim.Render("  Autodesk, Inc. This application is not affiliated with or"),
		styleItemDim.Render("  endorsed by Autodesk, Inc."),
		"",
		styleItemDim.Render("  https://aps.autodesk.com"),
		"",
		styleItemDim.Render("  [↑↓/jk] scroll  [a] close"),
	}

	// Scroll window
	visibleH := m.height - 2
	if visibleH < 1 {
		visibleH = 1
	}
	maxScroll := max(0, len(lines)-visibleH)
	scroll := clamp(m.aboutScroll, 0, maxScroll)

	end := min(scroll+visibleH, len(lines))
	var sb strings.Builder
	for _, l := range lines[scroll:end] {
		sb.WriteString(l)
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m Model) viewError() string {
	msg := "unknown error"
	if m.err != nil {
		msg = m.err.Error()
	}
	hint := "[r] Retry   [q] Quit"
	if isAuthError(m.err) {
		hint = "[r] Sign in again   [q] Quit"
	}
	content := styleError.Render("Error: " + msg + "\n\n" + hint)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

// isAuthError reports whether an error is almost certainly an expired or
// invalid access token, so the error screen can steer the user toward
// re-authenticating instead of a simple retry.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "token may be expired") ||
		strings.Contains(msg, "401")
}

// recoverFromError resets the model from stateError back to the beginning of
// the init flow. For auth errors it also deletes the on-disk token file so
// the next checkTokensCmd call is guaranteed to prompt for a fresh login
// instead of reusing a server-rejected token. For any other error it simply
// re-runs the same init sequence the process would run on startup.
func (m Model) recoverFromError() (Model, tea.Cmd) {
	if isAuthError(m.err) {
		_ = auth.DeleteTokens()
	}
	// Reset transient state so the UI comes back to a clean starting point.
	m.err = nil
	m.token = ""
	m.hubs = nil
	m.hubCursor = 0
	m.hubScroll = 0
	m.hubLoading = false
	m.selectedHubID = ""
	m.selectedHubAltID = ""
	m.selectedProjectAltID = ""
	m.selectedProjectWebURL = ""
	m.folderStack = nil
	m.cols = [numCols][]api.NavItem{}
	m.cursors = [numCols]int{}
	m.scrolls = [numCols]int{}
	m.loading = [numCols]bool{}
	m.activeCol = colProjects
	m.details = nil
	m.detailsLoading = false
	m.detailsScroll = 0
	m.statusMsg = ""
	m.state = stateLoading
	return m, tea.Batch(m.spinner.Tick, checkTokensCmd(m.clientID, m.clientSecret))
}

func (m Model) viewBrowser() string {
	// Reserve rows: 1 header + 2 footer (border+text) + 2 column border = 5
	const fixedRows = 5
	colHeight := m.height - fixedRows
	if colHeight < 3 {
		colHeight = 3
	}

	// 3-panel layout: Projects | Contents | Details
	// Details gets ~35% of the width; the 2 nav columns split the rest.
	detailsWidth := (m.width * 35) / 100
	navWidth := m.width - detailsWidth - 2
	colWidth := (navWidth - 4) / numCols
	if colWidth < 10 {
		colWidth = 10
	}
	cols := make([]string, numCols)
	titles := []string{"Projects", "Contents"}
	for i := 0; i < numCols; i++ {
		cols[i] = m.renderColumn(i, titles[i], colWidth, colHeight)
	}
	detailsCol := m.viewDetailsColumn(detailsWidth, colHeight)
	browserRow := lipgloss.JoinHorizontal(lipgloss.Top,
		append(cols, detailsCol)...)

	// Breadcrumb header: Hub › Project › Folder(s) › Document
	// The crumbs are built with buildBreadcrumb so the same logic drives
	// both the rendered string and the mouse hit-test regions.
	breadcrumb, _ := m.buildBreadcrumb(breadcrumbXOffset())
	headerParts := "FusionDataCLI"
	if breadcrumb != "" {
		headerParts += "  " + breadcrumb
	}
	if m.statusMsg != "" {
		headerParts += "  " + m.statusMsg
	}
	header := lipgloss.NewStyle().MaxWidth(m.width).Render(
		styleHeader.Render(headerParts),
	)

	// Footer
	mouseLabel := "[m] mouse:on"
	if !m.mouseEnabled {
		mouseLabel = "[m] mouse:off"
	}
	helpText := "[↑↓/jk] move  [←→/l] navigate  [h] hubs  [o] open  [r] refresh  [t] theme  " + mouseLabel + "  [a] about  [q] quit"
	// Right-align version by padding with spaces. Footer has border(1 top) + padding(0,1) so content width is width-4.
	contentWidth := m.width - 4
	gap := contentWidth - len(helpText) - len(m.version)
	if gap < 2 {
		gap = 2
	}
	footerLine := helpText + strings.Repeat(" ", gap) + m.version
	footer := styleFooter.Width(m.width - 2).Render(footerLine)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		browserRow,
		footer,
	)
}

func (m Model) renderColumn(col int, title string, width, height int) string {
	innerWidth := width - 4 // subtract border (2) + padding (2)
	if innerWidth < 4 {
		innerWidth = 4
	}

	var sb strings.Builder

	// Title row
	sb.WriteString(styleColumnTitle.Width(innerWidth).Render(title))
	sb.WriteString("\n")

	// Loading indicator
	if m.loading[col] {
		sb.WriteString(m.spinner.View())
		sb.WriteString(styleLoading.Render(" Loading…"))
	} else {
		items := m.cols[col]
		if len(items) == 0 {
			// Distinguish "never loaded" (nil) from "loaded but no content" (non-nil empty slice).
			if col == colContents && items != nil {
				sb.WriteString(styleEmpty.Width(innerWidth).Render("No designs found."))
				sb.WriteString("\n")
				sb.WriteString(styleEmpty.Width(innerWidth).Render("Project may contain legacy"))
				sb.WriteString("\n")
				sb.WriteString(styleEmpty.Width(innerWidth).Render("or non-Fusion content."))
			} else {
				sb.WriteString(styleEmpty.Width(innerWidth).Render("(empty)"))
			}
		} else {
			visibleRows := height - 3 // title + bottom margin
			if visibleRows < 1 {
				visibleRows = 1
			}
			scroll := m.scrolls[col]
			cursor := m.cursors[col]

			end := scroll + visibleRows
			if end > len(items) {
				end = len(items)
			}

			for i := scroll; i < end; i++ {
				item := items[i]
				label := itemLabel(item, innerWidth-2)

				active := col == m.activeCol
				selected := i == cursor

				var line string
				switch {
				case active && selected:
					line = styleItemSelected.Width(innerWidth).Render(label)
				case selected:
					line = styleItemNormal.Width(innerWidth).
						Foreground(colorAccent).
						Render(label)
				default:
					if item.IsContainer {
						line = styleContainerItem.Width(innerWidth).Render(label)
					} else {
						line = styleDocumentItem.Width(innerWidth).Render(label)
					}
				}
				sb.WriteString(line)
				if i < end-1 {
					sb.WriteString("\n")
				}
			}

			// Scroll indicators
			if scroll > 0 {
				sb.WriteString("\n" + styleItemDim.Render("  ↑ more"))
			}
			if end < len(items) {
				sb.WriteString("\n" + styleItemDim.Render("  ↓ more"))
			}
		}
	}

	content := sb.String()
	style := styleColumnInactive
	if col == m.activeCol {
		style = styleColumnActive
	}
	return style.Width(width).Height(height).Render(content)
}

// ---------------------------------------------------------------------------
// Details column
// ---------------------------------------------------------------------------

func (m Model) viewDetailsColumn(width, height int) string {
	inner := width - 4
	if inner < 4 {
		inner = 4
	}

	var sb strings.Builder
	sb.WriteString(styleColumnTitle.Width(inner).Render("Details"))
	sb.WriteString("\n")

	// A document is "actionable" in Fusion when the details panel is
	// populated for a non-container item. When true, we pin hint text for
	// the [f] open / [i] insert commands at the bottom of the panel so the
	// user knows these commands target this document.
	showFusionHints := !m.detailsLoading && m.details != nil
	hintReserved := 0
	if showFusionHints {
		hintReserved = 2 // blank separator + hint line
	}

	// Total lines available inside the column body (after title + borders).
	bodyH := height - 3
	if bodyH < 1 {
		bodyH = 1
	}
	// Space for scrollable details content, excluding reserved hint rows.
	visibleH := bodyH - hintReserved
	if visibleH < 1 {
		visibleH = 1
	}

	usedLines := 0
	if m.detailsLoading {
		sb.WriteString(m.spinner.View())
		sb.WriteString(styleLoading.Render(" Loading…"))
		usedLines = 1
	} else if m.details == nil {
		sb.WriteString(styleItemDim.Width(inner).Render("No item selected"))
		usedLines = 1
	} else {
		d := m.details
		lines := buildDetailLines(d, inner)
		scroll := clamp(m.detailsScroll, 0, max(0, len(lines)-visibleH))
		end := min(scroll+visibleH, len(lines))

		for i, l := range lines[scroll:end] {
			sb.WriteString(l)
			if i < end-scroll-1 {
				sb.WriteString("\n")
			}
			usedLines++
		}
		if scroll > 0 {
			sb.WriteString("\n" + styleItemDim.Render("  ↑ more"))
			usedLines++
		}
		if end < len(lines) {
			sb.WriteString("\n" + styleItemDim.Render("  ↓ more"))
			usedLines++
		}
	}

	if showFusionHints {
		// Pad with blank lines so the hint pins to the bottom of the panel.
		pad := visibleH - usedLines
		if pad < 0 {
			pad = 0
		}
		for i := 0; i < pad; i++ {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(styleItemDim.Width(inner).Render("[f] open in Fusion  [i] insert"))
	}

	return styleColumnInactive.Width(width).Height(height).Render(sb.String())
}

// buildDetailLines returns pre-rendered lines for the details panel.
func buildDetailLines(d *api.ItemDetails, width int) []string {
	label := func(k, v string) string {
		if v == "" {
			return ""
		}
		key := styleDetailKey.Render(k)
		return truncate(key+" "+v, width)
	}
	heading := func(s string) string {
		return styleColumnTitle.MarginBottom(0).Render(s + ":")
	}
	var lines []string
	add := func(s string) {
		if s != "" {
			lines = append(lines, s)
		}
	}

	// Name
	add(truncate(d.Name, width))
	add("")

	// Core metadata
	add(label("Size    ", formatSize(d.Size)))
	if d.VersionNumber > 0 {
		add(label("Version ", fmt.Sprintf("v%d", d.VersionNumber)))
	}
	add("")

	// Created / modified
	if !d.CreatedOn.IsZero() {
		add(heading("Created"))
		add(styleItemNormal.Render("  " + d.CreatedOn.Format("Jan 02 2006")))
		if d.CreatedBy != "" {
			add(styleItemNormal.Render("  " + d.CreatedBy))
		}
		add("")
	}
	if !d.ModifiedOn.IsZero() {
		add(heading("Modified"))
		add(styleItemNormal.Render("  " + d.ModifiedOn.Format("Jan 02 2006")))
		if d.ModifiedBy != "" {
			add(styleItemNormal.Render("  " + d.ModifiedBy))
		}
		add("")
	}

	// Design-specific fields
	if d.PartNumber != "" || d.PartDesc != "" || d.Material != "" {
		add(heading("Component"))
		add(label("Part No. ", d.PartNumber))
		add(label("Desc     ", d.PartDesc))
		add(label("Material ", d.Material))
		if d.IsMilestone {
			add(styleItemNormal.Render("  ★ Milestone"))
		}
		add("")
	}

	// Version history
	if len(d.Versions) > 0 {
		add(heading("Versions"))
		for i := len(d.Versions) - 1; i >= 0; i-- {
			if len(d.Versions)-1-i >= 10 {
				add(styleItemDim.Render(fmt.Sprintf("  … %d more", len(d.Versions)-10)))
				break
			}
			v := d.Versions[i]
			date := ""
			if !v.CreatedOn.IsZero() {
				date = v.CreatedOn.Format("Jan 02 2006")
			}
			header := fmt.Sprintf("  v%-3d  %s", v.Number, date)
			if v.CreatedBy != "" {
				header = truncate(header+"  "+v.CreatedBy, width)
			}
			add(styleItemNormal.Render(header))
			if v.Comment != "" {
				add(styleItemDim.Render(truncate("        "+v.Comment, width)))
			}
		}
		add("")
	}

	return lines
}

// formatSize converts a raw size string (bytes as string) to human-readable.
func formatSize(s string) string {
	if s == "" {
		return ""
	}
	var bytes int64
	if _, err := fmt.Sscanf(s, "%d", &bytes); err != nil {
		return s // not numeric; return as-is
	}
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (m Model) visibleRows() int {
	const fixedRows = 8 // header + footer + borders + title
	v := m.height - fixedRows
	if v < 1 {
		v = 1
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// itemLabel builds the display label for a nav item with a given max display width.
// Folders get a trailing "/" to distinguish them from documents.
func itemLabel(item api.NavItem, maxWidth int) string {
	icon := kindIcon(item.Kind)
	if item.Kind == "folder" {
		return truncate(icon+item.Name, maxWidth-1) + "/"
	}
	return truncate(icon+item.Name, maxWidth)
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// itemWebURL returns the best available web URL for an item.
// If the API provided a direct URL on the item, that is used first.
// Otherwise, falls back to constructing a URL from hub/project IDs.
func itemWebURL(item api.NavItem, hubAltID, projectAltID string) string {
	if item.WebURL != "" {
		return item.WebURL
	}
	if strings.HasPrefix(hubAltID, "b.") {
		return accURL(item, projectAltID)
	}
	return fusionURL(item, projectAltID)
}

// accURL returns the ACC web URL for an item.
func accURL(item api.NavItem, projectAltID string) string {
	const base = "https://acc.autodesk.com"
	switch item.Kind {
	case "hub":
		return base + "/"
	case "project":
		if item.AltID != "" {
			return base + "/docs/files/projects/" + item.AltID
		}
		return base + "/"
	case "folder", "design", "configured":
		if projectAltID != "" {
			return base + "/docs/files/projects/" + projectAltID
		}
		return base + "/"
	default:
		return base + "/"
	}
}

// fusionURL returns the Autodesk/Fusion web URL for a personal-hub item.
func fusionURL(item api.NavItem, projectAltID string) string {
	const base = "https://autodesk360.com"
	switch item.Kind {
	case "hub":
		return base + "/"
	case "project":
		if item.AltID != "" {
			return base + "/g/projects/" + item.AltID
		}
		return base + "/"
	case "folder", "design", "configured":
		if projectAltID != "" {
			return base + "/g/projects/" + projectAltID
		}
		return base + "/"
	default:
		return base + "/"
	}
}
