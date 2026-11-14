package schedule

import (
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
)

func loc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func at(t *testing.T, l *time.Location, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, l)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCronNext(t *testing.T) {
	utc := time.UTC
	lima := loc(t, "America/Lima")
	madrid := loc(t, "Europe/Madrid")
	cases := []struct {
		name, spec string
		l          *time.Location
		after      string
		want       string
	}{
		{"weekday standup", "0 9 * * 1-5", lima, "2025-01-03 09:00", "2025-01-06 09:00"}, // Fri → Mon
		{"same day later", "0 9 * * 1-5", lima, "2025-01-06 08:59", "2025-01-06 09:00"},
		{"strictly after", "30 14 * * *", utc, "2025-01-06 14:30", "2025-01-07 14:30"},
		{"step", "*/15 * * * *", utc, "2025-01-06 10:01", "2025-01-06 10:15"},
		{"range step", "10-40/10 8 * * *", utc, "2025-01-06 08:20", "2025-01-06 08:30"},
		{"open step", "5/20 * * * *", utc, "2025-01-06 10:06", "2025-01-06 10:25"},
		{"list", "0 8,12,18 * * *", utc, "2025-01-06 12:00", "2025-01-06 18:00"},
		{"month names", "0 0 1 jan,jul *", utc, "2025-02-01 00:00", "2025-07-01 00:00"},
		{"dow names", "0 7 * * mon-wed", utc, "2025-01-08 07:00", "2025-01-13 07:00"}, // Wed 8th → Mon 13th
		{"sunday as 7", "0 7 * * 7", utc, "2025-01-06 00:00", "2025-01-12 07:00"},
		{"sunday as 0", "0 7 * * 0", utc, "2025-01-06 00:00", "2025-01-12 07:00"},
		{"dom or dow", "0 0 13 * fri", utc, "2025-01-01 00:00", "2025-01-03 00:00"}, // Friday 3rd beats 13th
		{"dom only", "0 0 31 * *", utc, "2025-04-01 00:00", "2025-05-31 00:00"},
		{"leap day", "0 0 29 2 *", utc, "2025-01-01 00:00", "2028-02-29 00:00"},
		{"hourly", "@hourly", utc, "2025-01-06 10:20", "2025-01-06 11:00"},
		{"daily", "@daily", utc, "2025-01-06 10:20", "2025-01-07 00:00"},
		{"weekly", "@weekly", utc, "2025-01-06 10:20", "2025-01-12 00:00"},
		{"monthly", "@monthly", utc, "2025-01-06 10:20", "2025-02-01 00:00"},
		{"year end", "0 0 1 1 *", utc, "2025-12-31 23:59", "2026-01-01 00:00"},
		// Lima has no daylight saving: 9:00 is always UTC-5.
		{"lima fixed offset", "0 9 * * *", lima, "2025-03-30 00:00", "2025-03-30 09:00"},
		// Madrid, clocks forward 2025-03-30: 02:30 doesn't exist.
		{"madrid before the gap", "30 1 * * *", madrid, "2025-03-29 12:00", "2025-03-30 01:30"},
		{"madrid after the gap", "0 4 * * *", madrid, "2025-03-30 00:00", "2025-03-30 04:00"},
		// Clocks back 2025-10-26: 02:30 happens twice and runs once.
		{"madrid repeated hour", "30 2 * * *", madrid, "2025-10-25 12:00", "2025-10-26 02:30"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cr, err := ParseCron(c.spec)
			if err != nil {
				t.Fatal(err)
			}
			got := cr.Next(at(t, c.l, c.after), c.l)
			if want := at(t, c.l, c.want); !got.Equal(want) {
				t.Fatalf("Next(%s) = %s, want %s", c.after, got.In(c.l), want.In(c.l))
			}
		})
	}
}

func TestCronDaylightSaving(t *testing.T) {
	madrid := loc(t, "Europe/Madrid")
	cr, err := ParseCron("30 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	// Clocks forward (2025-03-30 02:00 → 03:00): the run happens once that day.
	day := time.Date(2025, 3, 29, 12, 0, 0, 0, madrid)
	first := cr.Next(day, madrid)
	if first.Day() != 30 || first.Sub(day) > 24*time.Hour {
		t.Fatalf("spring run: %s", first.In(madrid))
	}
	if next := cr.Next(first, madrid); next.Day() != 31 {
		t.Fatalf("after the spring run: %s", next.In(madrid))
	}
	// Clocks back (2025-10-26 03:00 → 02:00): it runs once, not twice.
	day = time.Date(2025, 10, 25, 12, 0, 0, 0, madrid)
	first = cr.Next(day, madrid)
	second := cr.Next(first, madrid)
	if first.Day() != 26 || second.Day() != 27 {
		t.Fatalf("autumn runs: %s then %s", first.In(madrid), second.In(madrid))
	}
	// A run at the repeated wall time is not repeated an hour later.
	if again := cr.Next(first.Add(time.Minute), madrid); again.Day() != 27 {
		t.Fatalf("repeated hour ran twice: %s", again.In(madrid))
	}
}

func TestParseCronRefuses(t *testing.T) {
	for _, spec := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *", "* * * 13 *", "* * * * 8",
		"5-1 * * * *", "*/0 * * * *", "a * * * *", "* * * foo *", "1,,2 * * * *", "0 0 31 2 *", "0 0 30 feb *",
		"@yearly", "@reboot",
	} {
		if _, err := ParseCron(spec); err == nil {
			t.Errorf("ParseCron(%q) accepted", spec)
		}
	}
}

func TestParseCronFiveMinuteFloor(t *testing.T) {
	for _, spec := range []string{"* * * * *", "*/4 * * * *", "0,3 * * * *"} {
		_, err := ParseCron(spec)
		if !errx.IsType(err, errx.TypeBusiness) {
			t.Errorf("ParseCron(%q): %v, want a Business refusal", spec, err)
		}
	}
	for _, spec := range []string{"*/5 * * * *", "0,5 * * * *", "0 * * * *", "0 9 * * 1-5", "*/10 9-17 * * *", "0,55 23 * * *"} {
		if _, err := ParseCron(spec); err != nil {
			t.Errorf("ParseCron(%q): %v", spec, err)
		}
	}
	// The wrap between days counts: 23:58 and 00:01 are three minutes apart.
	if _, err := ParseCron("1,58 0,23 * * *"); err == nil {
		t.Error("23:58 → 00:01 accepted")
	}
}

func TestCronString(t *testing.T) {
	c, err := ParseCron("  0   9 * *  1-5 ")
	if err != nil || c.String() != "0 9 * * 1-5" {
		t.Fatalf("%v %q", err, c.String())
	}
	if c, _ = ParseCron("@Daily"); c.String() != "0 0 * * *" {
		t.Fatalf("macro: %q", c.String())
	}
}
