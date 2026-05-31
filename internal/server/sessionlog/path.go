package sessionlog

import (
	"fmt"
	"path/filepath"
)

// TrajectoryFilename returns the log basename: "<user>.<conversation>.jsonl"
// with each id zero-padded to at least five digits.
func TrajectoryFilename(userID, conversationID int64) string {
	return fmt.Sprintf("%05d.%05d.jsonl", userID, conversationID)
}

// TrajectoryPath returns the full path under dir for a conversation trajectory log.
func TrajectoryPath(dir string, userID, conversationID int64) string {
	return filepath.Join(dir, TrajectoryFilename(userID, conversationID))
}
