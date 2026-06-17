package sessionlog

import (
	"fmt"
	"path/filepath"
)

// TrajectoryFilename returns the log basename: "<user>.<session>.jsonl"
// with each id zero-padded to at least five digits.
func TrajectoryFilename(userID, sessionID int64) string {
	return fmt.Sprintf("%05d.%05d.jsonl", userID, sessionID)
}

// TrajectoryPath returns the full path under dir for a session trajectory log.
func TrajectoryPath(dir string, userID, sessionID int64) string {
	return filepath.Join(dir, TrajectoryFilename(userID, sessionID))
}
