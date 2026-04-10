# FusionDataCLI

A terminal browser for [Autodesk Platform Services (APS)](https://aps.autodesk.com) Manufacturing Data Model. Navigate your Fusion hubs, projects, folders, and designs from the command line.

![FusionDataCLI screenshot](./docs/screenshot.png)

## Install

**Homebrew (macOS / Linux) — recommended**
```sh
brew install schneik80/fusiondatacli/fusiondatacli
```

Or download the latest binary for your platform from [Releases](https://github.com/schneik80/FusionDataCLI/releases).

**macOS (Apple Silicon)**
```sh
VERSION=$(curl -s https://api.github.com/repos/schneik80/FusionDataCLI/releases/latest | grep '"tag_name"' | cut -d'"' -f4 | tr -d v)
curl -L "https://github.com/schneik80/FusionDataCLI/releases/latest/download/FusionDataCLI-${VERSION}-darwin-arm64.tar.gz" | tar xz
sudo mv fusiondatacli /usr/local/bin/
```

**macOS (Intel)**
```sh
VERSION=$(curl -s https://api.github.com/repos/schneik80/FusionDataCLI/releases/latest | grep '"tag_name"' | cut -d'"' -f4 | tr -d v)
curl -L "https://github.com/schneik80/FusionDataCLI/releases/latest/download/FusionDataCLI-${VERSION}-darwin-amd64.tar.gz" | tar xz
sudo mv fusiondatacli /usr/local/bin/
```

**Linux (amd64)**
```sh
VERSION=$(curl -s https://api.github.com/repos/schneik80/FusionDataCLI/releases/latest | grep '"tag_name"' | cut -d'"' -f4 | tr -d v)
curl -L "https://github.com/schneik80/FusionDataCLI/releases/latest/download/FusionDataCLI-${VERSION}-linux-amd64.tar.gz" | tar xz
sudo mv fusiondatacli /usr/local/bin/
```

**Windows** — download `fusiondatacli-{version}-windows-amd64.zip` from the [Releases](https://github.com/schneik80/FusionDataCLI/releases) page and add the binary to your `PATH`.

## Usage

```sh
fusiondatacli
```

On first run the app opens your browser for Autodesk sign-in. After authenticating, navigate with keyboard or mouse:

| Key | Action |
|-----|--------|
| `↑` `↓` / `j` `k` | Move cursor |
| `→` `↵` / `l` | Enter folder / open details |
| `←` / `h` | Go back |
| `o` | Open selected document in browser (only after details have loaded) |
| `f` | Open in Fusion desktop (via Fusion MCP) |
| `i` | Insert into active Fusion design (via Fusion MCP) |
| `d` | Toggle details panel |
| `t` | Cycle color theme |
| `m` | Toggle mouse support on/off |
| `a` | About / license |
| `r` | Refresh |
| `?` | Debug log |
| `q` | Quit |

### Mouse support

Mouse support is enabled by default. Click items to select and navigate, use the scroll wheel to move through lists. Press `m` to toggle mouse on/off. The footer bar shows the current mouse state.

### Breadcrumb bar

The header displays a breadcrumb trail showing your current location in the hierarchy: Hub > Project > Folder(s) > Document. Each segment is clickable — click the hub to open the hub-select overlay, click a project or folder to jump back to that level.

### Fusion desktop integration

The `f` and `i` keys drive the running Fusion desktop client via its local MCP server (`http://127.0.0.1:27182/mcp`). `f` opens the selected document in a new Fusion window; `i` inserts it as an occurrence into the active design. Before sending the call, FusionDataCLI verifies that Fusion is on the same hub as the CLI (by matching the selected project against Fusion's active-hub project list) and refuses the call with a descriptive status message when the hubs differ.

### Opening documents in the browser

The `o` key opens the selected document in your default browser using the item-level `fusionWebUrl` permalink returned by the APS Manufacturing Data Model GraphQL API. This URL is loaded as part of the details panel, so `o` only fires **after** the details panel has finished loading for the selected item — the key hint `[o] web` appears at the bottom of the details panel while it's actionable. Pressing `o` on a container (project/folder) or before the details panel has loaded is a no-op with a status-bar hint.

The earlier hand-constructed fallback URLs (`https://autodesk360.com/g/projects/...`, `https://acc.autodesk.com/docs/files/projects/...`) have been removed — those patterns are rejected by Autodesk's team web app with a raw JSON `BROWSER_LOGIN_REQUIRED` error on team hubs. Only the per-item permalink from the Data Model API is trusted now.

The full URL passed to the OS browser handler is shown in the status bar so it can be inspected or copied.

### Non-US hubs

If your Fusion hub is in EMEA or Australia, set the region before running:

```sh
APS_REGION=EMEA fusiondatacli   # Europe, Middle East, Africa
APS_REGION=AUS  fusiondatacli   # Australia
```

## Details panel

Press `→` on any design to open the details panel. It shows:

- File size and current version number
- Created and last modified date and user
- Component metadata: part number, description, material, milestone flag
- Full version history with save comments

## Requirements

- An [Autodesk account](https://accounts.autodesk.com) with access to at least one Fusion Team hub
- macOS 12+, Linux, or Windows 10+
- Port `7879` available locally during sign-in (used for the OAuth callback)

## Building from source

Requires Go 1.22+.

```sh
git clone https://github.com/schneik80/FusionDataCLI
cd FusionDataCLI
```

**With your own APS app** (register a public client at [aps.autodesk.com/myapps](https://aps.autodesk.com/myapps), redirect URI `http://localhost:7879/callback`, scope `data:read`):

```sh
make build CLIENT_ID=your-client-id
```

Or store your client ID in a git-ignored file:

```sh
echo "your-client-id" > .aps-client-id
make build
```

**Local dev without an embedded client ID** (supply via env var at runtime):

```sh
make dev
APS_CLIENT_ID=your-client-id ./fusiondatacli
```

## License

[MIT](LICENSE) — © Kevin Schneider
