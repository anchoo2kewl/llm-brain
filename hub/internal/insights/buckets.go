package insights

import (
	"time"
)

// Range is a period the insights page can show.
type Range struct {
	Key   string        `json:"key"`
	Label string        `json:"label"`
	Dur   time.Duration `json:"-"` // 0: all time
}

var Ranges = []Range{
	{"24h", "24 hours", 24 * time.Hour},
	{"7d", "7 days", 7 * 24 * time.Hour},
	{"30d", "30 days", 30 * 24 * time.Hour},
	{"all", "All time", 0},
}

// ParseRange returns the named range, or 7d.
func ParseRange(key string) Range {
	for _, r := range Ranges {
		if r.Key == key {
			return r
		}
	}
	return Ranges[1]
}

// Buckets lays a period out for charts and says exactly which instants the totals cover.
//
// A bounded range (24h, 7d, 30d) is the exact window [now-duration, now): N equal rolling buckets of
// duration/N each, ending at now (24 hourly, 28 six-hourly, 30 daily), so totals and deltas compare equal
// windows. "All time" starts at the earliest data, aligned down to its bucket boundary (hour, day, or Monday),
// and counts calendar buckets through the one that holds now.
type Buckets struct {
	From, To time.Time // the window totals cover: From <= t < To
	N        int
	Step     time.Duration // fixed-length buckets (rolling ranges, or hourly/6-hourly all-time); 0 when Days > 0
	Days     int           // calendar days per bucket for all-time ranges (1 or 7)
	Loc      *time.Location
	Rolling  bool
}

// windowEnd is the exclusive end of a window that includes now (store times have millisecond precision).
func windowEnd(now time.Time) time.Time { return now.Truncate(time.Millisecond).Add(time.Millisecond) }

// NewBuckets lays out buckets for r ending at now. earliest is the first datum anywhere (all-time only).
func NewBuckets(r Range, now, earliest time.Time, loc *time.Location) Buckets {
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	end := windowEnd(now)
	if r.Dur > 0 {
		b := Buckets{Loc: loc, Rolling: true, To: end, From: end.Add(-r.Dur)}
		switch {
		case r.Dur <= 48*time.Hour:
			b.Step = time.Hour
		case r.Dur <= 8*24*time.Hour:
			b.Step = 6 * time.Hour
		default:
			b.Step = 24 * time.Hour
		}
		b.N = int(r.Dur / b.Step)
		return b
	}
	if earliest.IsZero() || earliest.After(now) {
		earliest = now
	}
	earliest = earliest.In(loc)
	span := now.Sub(earliest)
	b := Buckets{Loc: loc, To: end}
	switch {
	case span <= 48*time.Hour:
		b.Step = time.Hour
	case span <= 8*24*time.Hour:
		b.Step = 6 * time.Hour
	case span <= 92*24*time.Hour:
		b.Days = 1
	default:
		b.Days = 7
	}
	if b.Step > 0 {
		h := int(b.Step / time.Hour)
		b.From = time.Date(earliest.Year(), earliest.Month(), earliest.Day(), earliest.Hour()-earliest.Hour()%h, 0, 0, 0, loc)
		b.N = int(now.Sub(b.From)/b.Step) + 1
		return b
	}
	day := time.Date(earliest.Year(), earliest.Month(), earliest.Day(), 0, 0, 0, 0, loc)
	if b.Days == 7 {
		day = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)) // Monday
	}
	b.From = day
	b.N = daysBetween(day, now)/b.Days + 1
	return b
}

// Prev is the window of the same length immediately before b (bounded ranges only).
func (b Buckets) Prev() Buckets {
	p := b
	d := b.To.Sub(b.From)
	p.From, p.To = b.From.Add(-d), b.From
	return p
}

func civil(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func daysBetween(a, b time.Time) int { return int(civil(b).Sub(civil(a)).Hours() / 24) }

// In reports whether t falls in the window.
func (b Buckets) In(t time.Time) bool { return !t.Before(b.From) && t.Before(b.To) }

// Index is the bucket t falls in, or -1 outside the window.
func (b Buckets) Index(t time.Time) int {
	if !b.In(t) {
		return -1
	}
	var i int
	if b.Step > 0 {
		i = int(t.Sub(b.From) / b.Step)
	} else {
		i = daysBetween(b.From, t.In(b.Loc)) / b.Days
	}
	return max(0, min(b.N-1, i))
}

// BucketStart is the start of bucket i (BucketStart(N) is the end of the last one).
func (b Buckets) BucketStart(i int) time.Time {
	if b.Step > 0 {
		return b.From.Add(time.Duration(i) * b.Step)
	}
	return b.From.AddDate(0, 0, i*b.Days)
}

// Axis is the x axis of a report's charts: short labels for ticks and full ones for tooltips.
type Axis struct {
	Labels []string `json:"labels"`
	Tips   []string `json:"tips"`
}

func (b Buckets) Axis() Axis {
	a := Axis{Labels: make([]string, b.N), Tips: make([]string, b.N)}
	for i := 0; i < b.N; i++ {
		s, e := b.BucketStart(i).In(b.Loc), b.BucketStart(i+1).In(b.Loc)
		if e.After(b.To) {
			e = b.To
		}
		switch {
		case b.Step > 0 && b.Step < 24*time.Hour:
			a.Labels[i] = s.Format("15:04")
			if b.Step > time.Hour {
				a.Labels[i] = s.Format("Mon 15:04")
			}
			a.Tips[i] = s.Format("Mon Jan 2 15:04") + " – " + e.Format("15:04")
		case b.Step > 0: // rolling days
			a.Labels[i] = e.Format("Jan 2")
			a.Tips[i] = s.Format("Jan 2 15:04") + " – " + e.Format("Jan 2 15:04")
		case b.Days == 7:
			a.Labels[i] = "wk " + s.Format("Jan 2")
			a.Tips[i] = "Week of " + s.Format("Mon Jan 2, 2006")
		default:
			a.Labels[i] = s.Format("Jan 2")
			a.Tips[i] = s.Format("Mon Jan 2, 2006")
		}
	}
	return a
}

// hourKey is the UTC hour prefix of the store's time format ("2026-10-14T20"), for the activity table.
func hourKey(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

// parseHour reads an activity hour key.
func parseHour(s string) time.Time {
	t, _ := time.Parse("2006-01-02T15", s)
	return t
}

// hourMid places a whole-hour activity row at its midpoint, so each hour falls in exactly one of two adjacent
// windows (activity is only kept per hour).
func hourMid(h time.Time) time.Time { return h.Add(30 * time.Minute) }
