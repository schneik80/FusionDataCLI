# Architecture

FusionDataCLI is a single-binary terminal application written in Go. It authenticates with Autodesk Platform Services (APS), then renders a live three-column browser over the Manufacturing Data Model hierarchy using a reactive TUI loop.

---

## System Context

```mermaid
C4Context
    title FusionDataCLI — System Context

    Person(user, "Designer / Engineer", "Autodesk account holder with access to at least one Fusion Team hub")

    System(app, "FusionDataCLI", "Cross-platform terminal browser for APS Manufacturing Data Model. Runs entirely in the terminal — no GUI, no browser dependency after first login.")

    System_Ext(aps_auth, "APS Authentication v2", "OAuth 2.0 authorization server. Issues access and refresh tokens via PKCE 3-legged flow.")

    System_Ext(aps_mfg, "APS Manufacturing Data Model", "GraphQL API (v2). Exposes hubs, projects, folders, items, and version history for Fusion designs.")

    System_Ext(browser, "System Default Browser", "Used once during first login to complete the OAuth consent page. Not required after token is cached.")

    System_Ext(fusion, "Fusion Desktop", "Optional. Provides a local MCP server (http://127.0.0.1:27182/mcp) used to open and insert documents in the running app.")

    SystemDb_Ext(fs, "Local Filesystem", "~/.config/fusiondatacli/ — stores config.json (client ID) and tokens.json (access + refresh tokens).")

    Rel(user, app, "Navigates with keyboard")
    Rel(app, aps_auth, "PKCE OAuth login + token refresh", "HTTPS POST")
    Rel(app, aps_mfg, "GraphQL queries", "HTTPS POST")
    Rel(app, fs, "Reads config, reads/writes tokens")
    Rel(app, browser, "Opens auth URL on first login", "OS exec")
    Rel(app, fusion, "JSON-RPC tool calls (open / insert document)", "HTTP")
    Rel(browser, aps_auth, "Redirects to localhost:7879/callback")
```

---

## Container Diagram

```mermaid
C4Container
    title FusionDataCLI — Containers

    Person(user, "User")

    Container_Boundary(app, "FusionDataCLI (single binary)") {
        Component(main, "main", "Go — main.go", "CLI entry point. Loads config, wires packages, starts BubbleTea event loop with alternate-screen mode.")
        Component(config, "config", "Go package", "Three-layer config loader: env vars → config.json → build-time linker default. Resolves client ID and APS region.")
        Component(auth, "auth", "Go package", "Full OAuth 2.0 PKCE flow. Generates verifier/challenge, opens browser, runs local callback server, exchanges code for tokens, saves and refreshes token data.")
        Component(api, "api", "Go package", "Typed GraphQL client. Executes cursor-paginated queries for hubs, projects, folders, and items. Fetches rich item metadata and version history.")
        Component(ui, "ui", "Go package", "BubbleTea Model/Update/View. Three-column ranger-style browser with optional fourth details column. Three color themes. About and debug overlays.")
    }

    System_Ext(aps_auth, "APS Auth v2", "https://developer.api.autodesk.com/authentication/v2")
    System_Ext(aps_gql, "APS MFG GraphQL v2", "https://developer.api.autodesk.com/mfg/graphql")
    SystemDb_Ext(fs, "~/.config/fusiondatacli/")

    Rel(main, config, "Loads config")
    Rel(main, ui, "Creates Model, runs program")
    Rel(ui, auth, "Triggers login / token check")
    Rel(ui, api, "Issues data queries")
    Rel(auth, aps_auth, "PKCE token exchange + refresh", "HTTPS")
    Rel(auth, fs, "Persists tokens.json", "os.WriteFile 0600")
    Rel(config, fs, "Reads config.json", "os.ReadFile")
    Rel(api, aps_gql, "GraphQL POST", "HTTPS")
```

---

## Component Diagram — `ui` package

```mermaid
C4Component
    title ui package — Internal Components

    Component(app, "app.go", "BubbleTea Model", "Root state machine. Owns the Model struct, Init/Update/View lifecycle, all message handlers, navigation logic, and renderer orchestration.")
    Component(keys, "keys.go", "keyMap struct", "Declares all key bindings using charmbracelet/bubbles key package. Single keyMap var consumed by app.go Update loop.")
    Component(styles, "styles.go", "Theme + Lipgloss styles", "Defines colorTheme struct, three theme palettes (Rust, Mono, System), applyTheme() that rebuilds every Lipgloss style var, and cycleTheme() called on [t] keypress.")

    Rel(app, keys, "Reads key bindings")
    Rel(app, styles, "Calls cycleTheme(), reads style vars")
```

---

## Package Dependency Graph

```mermaid
graph TD
    main --> config
    main --> ui
    ui --> api
    ui --> auth
    auth --> config
    api --> config

    subgraph stdlib
        net/http
        crypto/sha256
        encoding/base64
        crypto/rand
        os
    end

    auth --> stdlib
    api --> stdlib

    subgraph charm["Charm.sh (external)"]
        bubbletea["charmbracelet/bubbletea"]
        bubbles["charmbracelet/bubbles"]
        lipgloss["charmbracelet/lipgloss"]
    end

    ui --> bubbletea
    ui --> bubbles
    ui --> lipgloss
```

---

## Data Flow — From Keypress to Screen

```mermaid
sequenceDiagram
    participant OS as Terminal / OS
    participant BT as BubbleTea runtime
    participant Update as Model.Update
    participant Cmd as tea.Cmd (goroutine)
    participant API as api package
    participant APS as APS GraphQL

    OS->>BT: KeyMsg (e.g. →)
    BT->>Update: Update(KeyMsg{→})
    Update->>Update: navigateRight()
    Update->>Cmd: loadItemsCmd(token, hubID, folderID)
    Cmd->>API: GetItems(ctx, token, hubID, folderID)
    API->>APS: POST /mfg/graphql
    APS-->>API: JSON response
    API-->>Cmd: []NavItem
    Cmd-->>BT: contentsLoadedMsg{items}
    BT->>Update: Update(contentsLoadedMsg)
    Update->>Update: populate cols[2], set loading=false
    BT->>BT: View() → render to terminal
```

---

## File Layout

```
FusionDataCLI/
├── main.go                  Entry point; wires config → ui; sets version ldflag
│
├── config/
│   └── config.go            Config struct, Load(), Dir(), Path(), DefaultClientID
│
├── auth/
│   ├── oauth.go             Login(), Refresh(), OpenBrowser(), PKCE helpers
│   ├── callback.go          WaitForCallback() — local HTTP server on :7879
│   └── tokens.go            LoadTokens(), SaveTokens(), TokenData.Valid()
│
├── api/
│   ├── client.go            gqlQuery(), NavItem struct, SetRegion(), EnableDebug()
│   ├── queries.go           GetHubs/Projects/Folders/Items; allPages() pagination
│   ├── details.go           GetItemDetails(), ItemDetails, VersionSummary, parseTime()
│   └── debug.go             dbgLog(), DebugLines(), DebugEnabled()
│
├── ui/
│   ├── app.go               Model, Init, Update, View; all state/nav/render logic
│   ├── keys.go              keyMap, keys var
│   └── styles.go            colorTheme, themes[], applyTheme(), cycleTheme()
│
├── docs/                    This documentation
├── .goreleaser.yaml         Build + release pipeline (goreleaser v2)
└── .github/workflows/
    └── release.yml          GitHub Actions: goreleaser + Homebrew tap on tag push
```
