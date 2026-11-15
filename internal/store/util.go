package store

import "time"

// nowMillis is the only clock this package reads. Every timestamp in the schema
// is Unix milliseconds, so no call site can disagree about the unit.
func nowMillis() int64 { return time.Now().UnixMilli() }
