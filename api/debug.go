package api

import (
	"fmt"
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
}
