package schedule

import (
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zones without the host's database (the image is distroless)

	"github.com/Abraxas-365/seaport/internal/errx"
)

// MinInterval is the finest cron a schedule may have: a prompt wakes an
// agent (and costs tokens), so "every minute" is almost certainly a typo.
const MinInterval = 5 * time.Minute

// Cron is a parsed five-field cron expression (minute hour day-of-month
// month day-of-week), evaluated in a time zone. Fields take `*`, numbers,
// ranges (`1-5`), lists (`1,3`), steps (`*/15`, `10-30/5`, `5/10`) and, for
// month and day of week, English names (`jan`, `mon`). Sunday is 0 or 7.
// The macros @hourly, @daily, @weekly and @monthly stand for their usual
// expressions. As in every cron, when both day-of-month and day-of-week
// are restricted a day matching either runs.
type Cron struct {
	spec             string
	minute           uint64
	hour             uint32
	dom              uint32 // bit n = day n (1..31)
	month            uint16 // bit n = month n (1..12)
	dow              uint8  // bit n = weekday n (0 = Sunday)
	domStar, dowStar bool
}

var macros = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron parses and validates an expression. A well-formed expression
// that can never fire (31 February) and one that fires more often than
// MinInterval are refused too.
func ParseCron(spec string) (Cron, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Cron{}, errx.InvalidField("cron", "cron is required")
	}
	if m, ok := macros[strings.ToLower(spec)]; ok {
		spec = m
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return Cron{}, errx.InvalidField("cron", "cron must have 5 fields: minute hour day-of-month month day-of-week")
	}
	c := Cron{spec: strings.Join(fields, " ")}
	var err error
	var v uint64
	if v, _, err = parseField(fields[0], 0, 59, nil); err != nil {
		return Cron{}, fieldErr("minute", err)
	}
	c.minute = v
	if v, _, err = parseField(fields[1], 0, 23, nil); err != nil {
		return Cron{}, fieldErr("hour", err)
	}
	c.hour = uint32(v)
	if v, c.domStar, err = parseField(fields[2], 1, 31, nil); err != nil {
		return Cron{}, fieldErr("day-of-month", err)
	}
	c.dom = uint32(v)
	if v, _, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return Cron{}, fieldErr("month", err)
	}
	c.month = uint16(v)
	// 7 is Sunday too: parse 0-7, then fold bit 7 into bit 0.
	if v, c.dowStar, err = parseField(fields[4], 0, 7, dowNames); err != nil {
		return Cron{}, fieldErr("day-of-week", err)
	}
	if v&(1<<7) != 0 {
		v |= 1
	}
	c.dow = uint8(v & 0x7f)
	if c.Next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC).IsZero() {
		return Cron{}, errx.InvalidField("cron", "cron never fires (that day does not exist in that month)")
	}
	if gap := c.minGap(); gap < MinInterval {
		return Cron{}, errx.Business("cron fires every " + gap.String() + ": at most once every " +
			MinInterval.String() + " is allowed")
	}
	return c, nil
}

func fieldErr(name string, err error) error {
	return errx.InvalidField("cron", "cron "+name+": "+err.Error())
}

// parseField reads one field into a bit set; star says it starts with `*`
// (as in Vixie cron, which decides the day-of-month/day-of-week rule).
func parseField(f string, lo, hi int, names map[string]int) (set uint64, star bool, err error) {
	if f == "" {
		return 0, false, errx.Validation("empty field")
	}
	star = strings.HasPrefix(f, "*")
	for _, part := range strings.Split(f, ",") {
		rng, step := part, 1
		if r, s, ok := strings.Cut(part, "/"); ok {
			rng = r
			if step, err = strconv.Atoi(s); err != nil || step < 1 {
				return 0, false, errx.Validation("bad step " + strconv.Quote(s))
			}
		}
		from, to := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			if from, err = number(a, lo, hi, names); err != nil {
				return 0, false, err
			}
			if to, err = number(b, lo, hi, names); err != nil {
				return 0, false, err
			}
			if from > to {
				return 0, false, errx.Validation("range " + strconv.Quote(rng) + " is backwards")
			}
		default:
			if from, err = number(rng, lo, hi, names); err != nil {
				return 0, false, err
			}
			if strings.Contains(part, "/") {
				to = hi // "5/10": from 5 to the end
			} else {
				to = from
			}
		}
		for n := from; n <= to; n += step {
			set |= 1 << uint(n)
		}
	}
	return set, star, nil
}

func number(s string, lo, hi int, names map[string]int) (int, error) {
	if n, ok := names[strings.ToLower(s)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errx.Validation("bad value " + strconv.Quote(s))
	}
	if n < lo || n > hi {
		return 0, errx.Validation(strconv.Itoa(n) + " is outside " + strconv.Itoa(lo) + "-" + strconv.Itoa(hi))
	}
	return n, nil
}

// String is the normalized expression (macros expanded).
func (c Cron) String() string { return c.spec }

// dayMatches applies the cron rule for the two day fields.
func (c Cron) dayMatches(t time.Time) bool {
	domOK := c.dom&(1<<uint(t.Day())) != 0
	dowOK := c.dow&(1<<uint(t.Weekday())) != 0
	if !c.domStar && !c.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// Next returns the first run strictly after the given instant, evaluating
// the expression on loc's wall clock, or the zero time when none comes
// within five years. Around daylight-saving changes: a wall time that
// doesn't exist (clocks forward) runs once that day, shifted by the gap
// (Go's time.Date rule), and a wall time that happens twice (clocks back)
// runs once, the first time.
func (c Cron) Next(after time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	// The search walks wall-clock fields as if they were UTC (no DST in the
	// way), then maps each candidate to an instant in loc.
	a := after.In(loc)
	wall := time.Date(a.Year(), a.Month(), a.Day(), a.Hour(), a.Minute(), 0, 0, time.UTC)
	limit := wall.Year() + 5
	for {
		wall = c.nextWall(wall, limit)
		if wall.IsZero() {
			return time.Time{}
		}
		t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, loc)
		if t.After(after) {
			return t
		}
	}
}

// nextWall is the first matching wall-clock minute after w (in UTC
// fields), or zero past the year limit.
func (c Cron) nextWall(w time.Time, limit int) time.Time {
	t := w.Add(time.Minute)
	for t.Year() <= limit {
		switch {
		case c.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case c.hour&(1<<uint(t.Hour())) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case c.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// minGap is the smallest distance between two runs of one day, counting
// the wrap from the last run to the first of the next day (conservative:
// a cron restricted to some days may never really run on two days in a row).
func (c Cron) minGap() time.Duration {
	var runs []int
	for h := 0; h < 24; h++ {
		if c.hour&(1<<uint(h)) == 0 {
			continue
		}
		for m := 0; m < 60; m++ {
			if c.minute&(1<<uint(m)) != 0 {
				runs = append(runs, h*60+m)
			}
		}
	}
	sort.Ints(runs)
	if len(runs) < 2 {
		return 24 * time.Hour
	}
	gap := runs[0] + 24*60 - runs[len(runs)-1]
	for i := 1; i < len(runs); i++ {
		gap = min(gap, runs[i]-runs[i-1])
	}
	return time.Duration(gap) * time.Minute
}
