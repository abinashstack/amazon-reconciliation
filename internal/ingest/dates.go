package ingest

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// paymentTimeLayouts are tried in order against the "body" (everything before
// " GMT..."). The payments file is NOT internally consistent about this:
// `date/time` always spells the month in full ("29 June 2026 5:39:32 pm"),
// but `Transaction Release Date` mixes full ("17 July 2026...") and
// three-letter-abbreviated month names ("1 Aug 2026...") - 1,153 of 20,498
// release dates use the abbreviated form. Both must be tried, not just the
// first-observed shape.
var paymentTimeLayouts = []string{
	"2 January 2006 3:04:05 pm",
	"2 January 2006 3:04:05 PM",
	"2 Jan 2006 3:04:05 pm",
	"2 Jan 2006 3:04:05 PM",
}

// parsePaymentTime parses the payments-file timestamp format:
//
//	"29 June 2026 5:39:32 pm GMT+9"
//	"17 July 2026 4:26:32 pm GMT+9"
//	"1 Aug 2026 4:59:36 am GMT+9"
//
// Go's reference parser cannot read the "GMT+9" zone, so we split it off and
// apply the offset by hand. The returned time is in UTC.
func parsePaymentTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	idx := strings.LastIndex(s, " GMT")
	if idx < 0 {
		return time.Time{}, fmt.Errorf("no GMT zone in %q", s)
	}
	body := strings.TrimSpace(s[:idx])
	off := strings.TrimSpace(s[idx+4:]) // "+9", "-7", "+09:30", ""
	var t time.Time
	var err error
	for _, layout := range paymentTimeLayouts {
		if t, err = time.Parse(layout, body); err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("payment time %q: unrecognised format", s)
	}
	loc := time.UTC
	if off != "" {
		secs, perr := parseOffset(off)
		if perr != nil {
			return time.Time{}, perr
		}
		loc = time.FixedZone("src", secs)
	}
	// reinterpret the wall-clock reading in the source zone, then to UTC
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc)
	return t.UTC(), nil
}

// parseOffset turns "+9", "-7", "+09:30" into seconds east of UTC.
func parseOffset(s string) (int, error) {
	sign := 1
	if strings.HasPrefix(s, "-") {
		sign = -1
	}
	s = strings.TrimLeft(s, "+-")
	var hh, mm int
	var err error
	if strings.Contains(s, ":") {
		parts := strings.SplitN(s, ":", 2)
		if hh, err = strconv.Atoi(parts[0]); err != nil {
			return 0, fmt.Errorf("bad offset %q", s)
		}
		if mm, err = strconv.Atoi(parts[1]); err != nil {
			return 0, fmt.Errorf("bad offset %q", s)
		}
	} else if hh, err = strconv.Atoi(s); err != nil {
		return 0, fmt.Errorf("bad offset %q", s)
	}
	return sign * (hh*3600 + mm*60), nil
}

// parseSettlementTime parses "17.07.2026 07:26:32 UTC" or "17.07.2026".
// Result is UTC.
func parseSettlementTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	s = strings.TrimSuffix(s, " UTC")
	for _, layout := range []string{"02.01.2006 15:04:05", "02.01.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("settlement time %q: unrecognised", s)
}

// dateOnly truncates to midnight UTC (zero time stays zero).
func dateOnly(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
