package freeturn

import (
	"regexp"
	"strings"
	"time"
)

// freeturn binaries log as "2006/01/02 15:04:05" in the router's local TZ.
var freeturnLogTimeRE = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})`)

func parseFreeturnLogTime(line string) (time.Time, bool) {
	m := freeturnLogTimeRE.FindStringSubmatch(strings.TrimSpace(line))
	if len(m) < 2 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006/01/02 15:04:05", m[1], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
