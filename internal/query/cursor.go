package query

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Cursors are "<timestamp_ms>-<id>" of the last row on the previous page.
func EncodeCursor(timestampMS, id int64) string {
	return fmt.Sprintf("%d-%d", timestampMS, id)
}

func DecodeCursor(value string) (int64, int64, error) {
	rawMS, rawID, found := strings.Cut(value, "-")
	timestampMS, msErr := strconv.ParseInt(rawMS, 10, 64)
	id, idErr := strconv.ParseInt(rawID, 10, 64)
	if !found || msErr != nil || idErr != nil || timestampMS <= 0 || id <= 0 {
		return 0, 0, errors.New("invalid cursor")
	}
	return timestampMS, id, nil
}
