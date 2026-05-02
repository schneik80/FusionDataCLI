package api

import (
	"fmt"
	"os"
	"sync"
	"time"
)

const maxDebugLines = 500

var (
	dbgMu      sync.Mutex
	dbgEnabled bool
	dbgLines   []string
)

// EnableDebug turns on request/response logging.
func EnableDebug() {
	dbgMu.Lock()
	defer dbgMu.Unlock()
	dbgEnabled = true
}

// DebugEnabled reports whether debug logging is active.
func DebugEnabled() bool {
	dbgMu.Lock()
	defer dbgMu.Unlock()
	return dbgEnabled
}

// DebugLines returns a snapshot of the log.
func DebugLines() []string {
	dbgMu.Lock()
	defer dbgMu.Unlock()
	cp := make([]string, len(dbgLines))
	copy(cp, dbgLines)
	return cp
}

func dbgLog(format string, args ...any) {
	dbgMu.Lock()
	defer dbgMu.Unlock()
	if !dbgEnabled {
		return
	}
	line := fmt.Sprintf("[%s] ", time.Now().Format("15:04:05")) + fmt.Sprintf(format, args...)
	dbgLines = append(dbgLines, line)
	if len(dbgLines) > maxDebugLines {
		dbgLines = dbgLines[len(dbgLines)-maxDebugLines:]
	}
	// Mirror to stderr so users can `2> file.log` to capture without
	// scrolling the in-app overlay (which is unreachable from stateError).
	fmt.Fprintln(os.Stderr, line)
}

// DebugLog appends a formatted line to the shared debug log. Intended for
// use by other packages (e.g. the ui layer) so that externally-initiated
// events like "browser opened with URL X" are captured alongside the
// request/response entries emitted by this package.
func DebugLog(format string, args ...any) {
	dbgLog(format, args...)
}
