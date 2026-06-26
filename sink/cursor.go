package main

import (
	"fmt"
	"os"

	"github.com/streamingfast/substreams/sink"
)

// loadCursor reads the persisted cursor from disk. A missing file means "start
// from the beginning" (a blank cursor).
func loadCursor(path string) (*sink.Cursor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sink.NewBlankCursor(), nil
		}
		return nil, fmt.Errorf("read cursor file %q: %w", path, err)
	}

	cursor, err := sink.NewCursor(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse cursor from %q: %w", path, err)
	}
	return cursor, nil
}

// persistCursor writes the cursor to disk.
//
// REVISIT / complete here: this is intentionally the simplest possible cursor
// store for a demo. A production sink should instead:
//   - write atomically (write to a temp file, then os.Rename) so a crash mid-
//     write cannot leave a truncated cursor, AND
//   - write the cursor in the SAME atomic unit (DB transaction) as the data it
//     just produced, so data and cursor can never disagree after a crash.
//
// See substreams-sink-sql for a battle-tested implementation.
func persistCursor(path string, cursor *sink.Cursor) error {
	if err := os.WriteFile(path, []byte(cursor.String()), 0o644); err != nil {
		return fmt.Errorf("write cursor file %q: %w", path, err)
	}
	return nil
}
