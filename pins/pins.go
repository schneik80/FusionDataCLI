package pins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/schneik80/FusionDataCLI/config"
)

// FolderRef is a single hop in a folder ancestry chain (mirrors api.FolderRef).
type FolderRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Pin represents a bookmarked hub item (project, folder, or document).
// ProjectID and FolderPath are captured at pin time so navigation to
// projects and folders doesn't require an API call.
type Pin struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Kind         string      `json:"kind"`
	HubID        string      `json:"hub_id"`
	ProjectID    string      `json:"project_id,omitempty"`
	ProjectAltID string      `json:"project_alt_id,omitempty"`
	// FolderPath is the ancestor chain from project root to the item:
	//   - project: empty
	//   - folder:  root-to-leaf path including the folder itself
	//   - document: root-to-leaf path of the containing folder
	FolderPath   []FolderRef `json:"folder_path,omitempty"`
	PinnedAt     time.Time   `json:"pinned_at"`
}

func pinsPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pins.json"), nil
}

// Load reads pinned items from disk. Returns an empty slice when the file is
// absent or corrupt rather than propagating an error that would block startup.
func Load() ([]Pin, error) {
	path, err := pinsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Pin{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ps []Pin
	if err := json.Unmarshal(data, &ps); err != nil {
		return []Pin{}, nil
	}
	return ps, nil
}

// Save writes the pin list to disk with mode 0600.
func Save(ps []Pin) error {
	path, err := pinsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// IsPinned reports whether the given item ID is in the pin list.
func IsPinned(ps []Pin, id string) bool {
	for _, p := range ps {
		if p.ID == id {
			return true
		}
	}
	return false
}

// Add prepends p to ps unless its ID is already present.
func Add(ps []Pin, p Pin) []Pin {
	if IsPinned(ps, p.ID) {
		return ps
	}
	p.PinnedAt = time.Now()
	return append([]Pin{p}, ps...)
}

// Remove returns a new slice with the item matching id omitted.
func Remove(ps []Pin, id string) []Pin {
	out := ps[:0:0]
	for _, p := range ps {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

// IsPinnable reports whether an item of the given kind may be pinned.
// Hubs are excluded; projects, folders, and document kinds are allowed.
func IsPinnable(kind string) bool {
	switch kind {
	case "project", "folder", "design", "drawing", "configured":
		return true
	}
	return false
}
