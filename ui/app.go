package ui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/schneik80/FusionDataCLI/api"
	"github.com/schneik80/FusionDataCLI/auth"
	"github.com/schneik80/FusionDataCLI/config"
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
	stateBrowsing                    // main 3-column browser
	stateAbout                       // about / license overlay
	stateDebug                       // debug log overlay
	stateError                       // unrecoverable error
)

// Column indices
const (
	colHubs     = 0
	colProjects = 1
	colContents = 2
	numCols     = 3
)

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type (
	tokenReadyMsg     struct{ token string }
	hubsLoadedMsg     struct{ items []api.NavItem }
	projectsLoadedMsg struct{ items []api.NavItem }
	contentsLoadedMsg struct{ items []api.NavItem }
	detailsLoadedMsg  struct{ details *api.ItemDetails }
	errMsg            struct{ err error }
	openedBrowserMsg  struct{}
)

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

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

	// Column data (hubs=0, projects=1, folders+items=2)
	cols    [numCols][]api.NavItem
	cursors [numCols]int
	loading [numCols]bool
	// scroll offsets for each column (for long lists)
	scrolls [numCols]int

	// Which column has keyboard focus
	activeCol int

	// Details panel
	detailsOpen    bool
	detailsLoading bool
	details        *api.ItemDetails
	detailsScroll  int

	// About / debug overlay scroll
	aboutScroll int
	debugScroll int

	// For column 2: when drilling into a subfolder, track the stack so we can go back.
	// Each entry is the folder ID whose contents are currently shown.
	folderStack []string

	// IDs and URLs of the currently selected hub and project.
	selectedHubID         string
	selectedHubAltID      string
	selectedHubWebURL     string // fusionWebUrl of the selected hub, used for desktop links
	selectedProjectAltID  string
	selectedProjectWebURL string // fusionWebUrl of the selected project, used as URL fallback

	spinner spinner.Model
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
			state:   stateSetupNeeded,
			err:     cfgErr,
			spinner: sp,
			version: version,
		}
	}

	return Model{
		state:        stateLoading,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		spinner:      sp,
		version:      version,
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

// openDesktopCmd builds a fusion360:// deep-link and opens it in the OS handler,
// which launches the Fusion desktop client and opens the document directly.
// Mirrors the Python logic in the PowerTools-Share-Document add-in:
//
//	fusion360://lineageUrn=<encoded-id>&hubUrl=<encoded-hub-url>&documentName=<encoded-name>
//
// hubBaseURL is the hub's fusionWebUrl (e.g. "https://autodesk8083.autodesk360.com").
// Per the Python add-in, spaces are removed, trailing chars matching the last 3 are
// stripped, then the result is uppercased before URL-encoding.
func openDesktopCmd(itemID, itemName, hubBaseURL string) tea.Cmd {
	return func() tea.Msg {
		stripped := strings.TrimRight(strings.ReplaceAll(hubBaseURL, " ", ""), hubBaseURL[max(0, len(hubBaseURL)-3):])
		hubURLParam := strings.ToUpper(stripped)

		link := "fusion360://lineageUrn=" + url.QueryEscape(itemID) +
			"&hubUrl=" + url.QueryEscape(hubURLParam) +
			"&documentName=" + url.QueryEscape(itemName)
		_ = auth.OpenBrowser(link)
		return openedBrowserMsg{}
	}
}

// openViewerCmd opens the web viewer for an item.
// The viewer URL is derived from the overview URL by replacing "/overview/" with "/viewer/".
func openViewerCmd(overviewURL string) tea.Cmd {
	return func() tea.Msg {
		viewerURL := strings.ReplaceAll(overviewURL, "/overview/", "/viewer/")
		_ = auth.OpenBrowser(viewerURL)
		return openedBrowserMsg{}
	}
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
		m.loading[colHubs] = true
		return m, loadHubsCmd(m.token)

	case hubsLoadedMsg:
		m.loading[colHubs] = false
		m.cols[colHubs] = msg.items
		m.cursors[colHubs] = 0
		m.scrolls[colHubs] = 0
		m.state = stateBrowsing
		// Auto-load projects if only one hub
		if len(msg.items) == 1 {
			m.activeCol = colProjects
			m.loading[colProjects] = true
			return m, loadProjectsCmd(m.token, msg.items[0].ID)
		}
		m.activeCol = colHubs
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
		return m, nil

	case detailsLoadedMsg:
		m.detailsLoading = false
		m.details = msg.details
		m.detailsScroll = 0
		return m, nil

	case openedBrowserMsg:
		m.statusMsg = "Opened in browser"
		return m, nil

	case errMsg:
		m.err = msg.err
		m.state = stateError
		return m, nil

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

	case m.state != stateBrowsing:
		return m, nil

	case key.Matches(msg, keys.Up):
		m.moveCursor(-1)
		if m.detailsOpen {
			m.detailsScroll = 0
			return m, m.maybeLoadDetails()
		}

	case key.Matches(msg, keys.Down):
		m.moveCursor(1)
		if m.detailsOpen {
			m.detailsScroll = 0
			return m, m.maybeLoadDetails()
		}

	case key.Matches(msg, keys.Left):
		return m.navigateLeft()

	case key.Matches(msg, keys.Right), key.Matches(msg, keys.Enter):
		return m.navigateRight()

	case key.Matches(msg, keys.Details):
		return m.toggleDetails()

	case key.Matches(msg, keys.Open):
		return m.openInBrowser()

	case key.Matches(msg, keys.OpenDesktop):
		return m.openInDesktop()

	case key.Matches(msg, keys.OpenViewer):
		return m.openInViewer()

	case key.Matches(msg, keys.Refresh):
		return m.refresh()
	}

	return m, nil
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

// navigateLeft moves focus left or goes up a folder level, returning a reload
// command when the folder stack is popped.
func (m Model) navigateLeft() (Model, tea.Cmd) {
	switch m.activeCol {
	case colContents:
		m.detailsOpen = false
		if len(m.folderStack) > 0 {
			// Pop folder stack and reload the parent's contents.
			m.folderStack = m.folderStack[:len(m.folderStack)-1]
			m.cols[colContents] = nil
			m.loading[colContents] = true
			if len(m.folderStack) > 0 {
				// Reload the folder that's now on top of the stack.
				return m, loadItemsCmd(m.token, m.selectedHubID, m.folderStack[len(m.folderStack)-1])
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
		m.activeCol = colHubs
	case colHubs:
		// Already at leftmost column.
	}
	return m, nil
}

// navigateRight moves focus right, loading the next level.
func (m Model) navigateRight() (Model, tea.Cmd) {
	switch m.activeCol {
	case colHubs:
		item := m.selectedItem(colHubs)
		if item == nil {
			return m, nil
		}
		m.selectedHubID = item.ID
		m.selectedHubAltID = item.AltID
		m.selectedHubWebURL = item.WebURL
		m.activeCol = colProjects
		m.cols[colProjects] = nil
		m.cols[colContents] = nil
		m.loading[colProjects] = true
		return m, loadProjectsCmd(m.token, item.ID)

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
		if !item.IsContainer {
			// Open details panel for documents.
			return m.toggleDetails()
		}
		// Drill into sub-folder.
		m.folderStack = append(m.folderStack, item.ID)
		m.cols[colContents] = nil
		m.loading[colContents] = true
		return m, loadItemsCmd(m.token, m.selectedHubID, item.ID)
	}
	return m, nil
}

// toggleDetails opens or closes the details panel for the currently selected item.
func (m Model) toggleDetails() (Model, tea.Cmd) {
	if m.detailsOpen {
		m.detailsOpen = false
		return m, nil
	}
	item := m.selectedItem(m.activeCol)
	if item == nil || item.IsContainer {
		return m, nil
	}
	m.detailsOpen = true
	m.details = nil
	m.detailsLoading = true
	m.detailsScroll = 0
	return m, loadDetailsCmd(m.token, m.selectedHubID, item.ID)
}

// maybeLoadDetails reloads details for the current item if the panel is open.
func (m Model) maybeLoadDetails() tea.Cmd {
	item := m.selectedItem(m.activeCol)
	if item == nil || item.IsContainer {
		m.detailsOpen = false
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

// openInDesktop launches the Fusion desktop client via the fusion360:// protocol.
// Requires the details panel to have been loaded (for the item URL / ID).
func (m Model) openInDesktop() (Model, tea.Cmd) {
	item := m.selectedItem(m.activeCol)
	if item == nil || item.IsContainer || m.selectedHubWebURL == "" {
		return m, nil
	}
	m.statusMsg = "Opening in Fusion…"
	return m, openDesktopCmd(item.ID, item.Name, m.selectedHubWebURL)
}

// openInViewer opens the web viewer for the currently selected design item.
// Uses the fusionWebUrl from the loaded details panel and replaces /overview/ with /viewer/.
func (m Model) openInViewer() (Model, tea.Cmd) {
	item := m.selectedItem(m.activeCol)
	if item == nil || item.IsContainer {
		return m, nil
	}
	webURL := ""
	if m.details != nil && m.details.FusionWebURL != "" {
		webURL = m.details.FusionWebURL
	} else if m.selectedProjectWebURL != "" {
		webURL = m.selectedProjectWebURL
	}
	if webURL == "" {
		return m, nil
	}
	m.statusMsg = "Opening viewer…"
	return m, openViewerCmd(webURL)
}

// refresh reloads the data for the active column.
func (m Model) refresh() (Model, tea.Cmd) {
	switch m.activeCol {
	case colHubs:
		m.cols[colHubs] = nil
		m.loading[colHubs] = true
		return m, loadHubsCmd(m.token)

	case colProjects:
		hub := m.selectedItem(colHubs)
		if hub == nil {
			return m, nil
		}
		m.cols[colProjects] = nil
		m.loading[colProjects] = true
		return m, loadProjectsCmd(m.token, hub.ID)

	case colContents:
		if len(m.folderStack) > 0 {
			// Reload current folder
			folderID := m.folderStack[len(m.folderStack)-1]
			m.cols[colContents] = nil
			m.loading[colContents] = true
			return m, loadItemsCmd(m.token, m.selectedHubID, folderID)
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
	content := styleError.Render("Error: " + msg + "\n\n[q] Quit")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

func (m Model) viewBrowser() string {
	// Reserve rows: 1 header + 1 footer + 2 border = 4
	const fixedRows = 4
	colHeight := m.height - fixedRows
	if colHeight < 3 {
		colHeight = 3
	}

	var browserRow string
	if m.detailsOpen {
		// 4-column layout: Hubs | Projects | Contents | Details
		// Details gets ~40% of the width; the 3 nav columns split the rest.
		detailsWidth := (m.width * 2) / 5
		navWidth := m.width - detailsWidth - 2
		colWidth := (navWidth - 6) / numCols
		if colWidth < 8 {
			colWidth = 8
		}
		cols := make([]string, numCols)
		titles := []string{"Hubs", "Projects", "Contents"}
		for i := 0; i < numCols; i++ {
			cols[i] = m.renderColumn(i, titles[i], colWidth, colHeight)
		}
		detailsCol := m.viewDetailsColumn(detailsWidth, colHeight)
		browserRow = lipgloss.JoinHorizontal(lipgloss.Top,
			append(cols, detailsCol)...)
	} else {
		colWidth := (m.width - 6) / numCols
		if colWidth < 10 {
			colWidth = 10
		}
		cols := make([]string, numCols)
		titles := []string{"Hubs", "Projects", "Contents"}
		for i := 0; i < numCols; i++ {
			cols[i] = m.renderColumn(i, titles[i], colWidth, colHeight)
		}
		browserRow = lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	}

	// Header
	status := ""
	if m.statusMsg != "" {
		status = styleStatus.Render(" — " + m.statusMsg)
	}
	header := styleHeader.Render("FusionDataCLI") + status

	// Footer
	footer := styleFooter.Width(m.width - 2).Render(
		"[↑↓/jk] move  [←→/hl] navigate  [o] open  [f] Fusion  [v] viewer  [r] refresh  [a] about  [q] quit",
	)

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
				sb.WriteString(styleItemDim.Width(innerWidth).Render("No designs found."))
				sb.WriteString("\n")
				sb.WriteString(styleItemDim.Width(innerWidth).Render("Project may contain legacy"))
				sb.WriteString("\n")
				sb.WriteString(styleItemDim.Width(innerWidth).Render("or non-Fusion content."))
			} else {
				sb.WriteString(styleItemDim.Width(innerWidth).Render("(empty)"))
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
					line = styleItemNormal.Width(innerWidth).Render(label)
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

	if m.detailsLoading {
		sb.WriteString(m.spinner.View())
		sb.WriteString(styleLoading.Render(" Loading…"))
	} else if m.details == nil {
		sb.WriteString(styleItemDim.Width(inner).Render("No item selected"))
	} else {
		d := m.details
		lines := buildDetailLines(d, inner)

		visibleH := height - 3
		if visibleH < 1 {
			visibleH = 1
		}
		scroll := clamp(m.detailsScroll, 0, max(0, len(lines)-visibleH))
		end := min(scroll+visibleH, len(lines))

		for _, l := range lines[scroll:end] {
			sb.WriteString(l)
			sb.WriteString("\n")
		}
		if scroll > 0 {
			sb.WriteString(styleItemDim.Render("  ↑ more"))
		}
		if end < len(lines) {
			sb.WriteString("\n" + styleItemDim.Render("  ↓ more"))
		}
	}

	return styleColumnInactive.Width(width).Height(height).Render(sb.String())
}

// buildDetailLines returns pre-rendered lines for the details panel.
func buildDetailLines(d *api.ItemDetails, width int) []string {
	label := func(k, v string) string {
		if v == "" {
			return ""
		}
		key := styleItemDim.Render(k)
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
		for i, v := range d.Versions {
			if i >= 10 {
				add(styleItemDim.Render(fmt.Sprintf("  … %d more", len(d.Versions)-10)))
				break
			}
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
