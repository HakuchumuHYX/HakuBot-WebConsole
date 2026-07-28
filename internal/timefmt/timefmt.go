package timefmt

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
)

const Layout = "2006-1-2 15:04:05"

var Location = mustLocation()

func mustLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return location
}

func FormatMilliseconds(milliseconds int64) string {
	return time.UnixMilli(milliseconds).In(Location).Format(Layout)
}

func FormatTime(value time.Time) string {
	return value.In(Location).Format(Layout)
}

func Parse(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := time.ParseInLocation(Layout, trimmed, Location)
	if err != nil {
		return time.Time{}, fmt.Errorf(
			"time must use %s in GMT+8: %w",
			Layout,
			err,
		)
	}
	if parsed.Format(Layout) != trimmed {
		return time.Time{}, fmt.Errorf("time is not canonical GMT+8 format")
	}
	return parsed, nil
}
