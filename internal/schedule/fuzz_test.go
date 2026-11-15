package schedule

import (
	"testing"
	"time"
)

// FuzzParseCron: no expression panics the parser; an accepted one fires,
// moves forward, never more often than MinInterval and prints as an
// expression that parses to the same schedule.
func FuzzParseCron(f *testing.F) {
	for _, s := range []string{
		"", "* * * * *", "*/5 * * * *", "0 9 * * mon-fri", "@daily", "@HOURLY", "30 2 31 feb *", "0 0 29 2 *",
		"5/10 1-3 1,15 jan,dec sun", "0 0 * * 7", "0,5,10 * * * *", "0 0 1 1 *", "*/0 * * * *", "99 * * * *",
		"1-5/2 */3 * * *", "0 0 31 4,6,9,11 *", "  0   12 *  *  1 ", "a b c d e", "0 0 * * 1-7/3", "-1 * * * *",
		"0 24 * * *", "0 0 0 * *", "*/5 * * * * *", "0 0 * jan-dec/5 *",
	} {
		f.Add(s)
	}
	zones := []*time.Location{time.UTC, time.FixedZone("x", -5*3600), time.FixedZone("y", 5*3600+1800)}
	if l, err := time.LoadLocation("America/Lima"); err == nil {
		zones = append(zones, l)
	}
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		zones = append(zones, l)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		c, err := ParseCron(spec)
		if err != nil {
			return
		}
		after := time.Date(2025, 3, 29, 23, 58, 30, 0, time.UTC)
		for _, loc := range zones {
			next := c.Next(after, loc)
			if next.IsZero() || !next.After(after) {
				t.Fatalf("%q: next %v after %v in %v", spec, next, after, loc)
			}
		}
		// In UTC (no daylight-saving shifts) consecutive runs keep MinInterval.
		prev := c.Next(after, time.UTC)
		for i := 0; i < 6; i++ {
			n := c.Next(prev, time.UTC)
			if n.IsZero() {
				break
			}
			if n.Sub(prev) < MinInterval {
				t.Fatalf("%q: runs %v and %v are closer than %v", spec, prev, n, MinInterval)
			}
			prev = n
		}
		again, err := ParseCron(c.String())
		if err != nil || again.String() != c.String() || again.Next(after, time.UTC) != c.Next(after, time.UTC) {
			t.Fatalf("%q does not round trip through %q: %v", spec, c.String(), err)
		}
	})
}
