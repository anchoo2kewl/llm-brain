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

// Buckets splits a period into equal, local-time-aligned buckets: hours, 6-hour blocks, days or weeks.
type Buckets struct {
	Start time.Time // first bucket's start (local)
	End   time.Time // last bucket's end
	N     int
	Step  time.Duration // for sub-day steps; 0 when Days > 0
	Days  int           // 1 (daily) or 7 (weekly)
	Loc   *time.Location
}

// NewBuckets lays out buckets for r ending with the bucket that holds now. earliest bounds the "all" range.
func NewBuckets(r Range, now, earliest time.Time, loc *time.Location) Buckets {
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	dur := r.Dur
	if dur == 0 {
		if earliest.IsZero() || earliest.After(now) {
			earliest = now.Add(-7 * 24 * time.Hour)
		}
		dur = now.Sub(earliest) + time.Hour
	}
	b := Buckets{Loc: loc}
	switch {
	case dur <= 48*time.Hour:
		b.Step = time.Hour
	case dur <= 8*24*time.Hour:
		b.Step = 6 * time.Hour
	case dur <= 92*24*time.Hour:
		b.Days = 1
	default:
		b.Days = 7
	}
	if b.Step > 0 {
		h := now.Hour() - now.Hour()%int(b.Step/time.Hour)
		cur := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, loc)
		b.N = int((dur + b.Step - 1) / b.Step)
		b.Start = cur.Add(-time.Duration(b.N-1) * b.Step)
		b.End = cur.Add(b.Step)
		return b
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if b.Days == 7 {
		day = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)) // Monday
	}
	span := int(dur.Hours()/24) + 1
	b.N = (span + b.Days - 1) / b.Days
	b.Start = day.AddDate(0, 0, -(b.N-1)*b.Days)
	b.End = day.AddDate(0, 0, b.Days)
	return b
}

// Prev is the same number of buckets immediately before b.
func (b Buckets) Prev() Buckets {
	p := b
	if b.Step > 0 {
		p.Start = b.Start.Add(-time.Duration(b.N) * b.Step)
	} else {
		p.Start = b.Start.AddDate(0, 0, -b.N*b.Days)
	}
	p.End = b.Start
	return p
}

func civil(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Index is the bucket t falls in, or -1 outside.
func (b Buckets) Index(t time.Time) int {
	if t.Before(b.Start) || !t.Before(b.End) {
		return -1
	}
	t = t.In(b.Loc)
	var i int
	if b.Step > 0 {
		i = int(t.Sub(b.Start) / b.Step)
	} else {
		days := int(civil(t).Sub(civil(b.Start)).Hours() / 24)
		i = days / b.Days
	}
	if i < 0 || i >= b.N {
		return -1
	}
	return i
}

// BucketStart is the start of bucket i.
func (b Buckets) BucketStart(i int) time.Time {
	if b.Step > 0 {
		return b.Start.Add(time.Duration(i) * b.Step)
	}
	return b.Start.AddDate(0, 0, i*b.Days)
}

// Labels names each bucket for axes and tooltips.
func (b Buckets) Labels() []string {
	out := make([]string, b.N)
	for i := range out {
		t := b.BucketStart(i)
		switch {
		case b.Step == time.Hour:
			out[i] = t.Format("15:04")
		case b.Step > 0:
			out[i] = t.Format("Mon 15:04")
		case b.Days == 7:
			out[i] = "wk " + t.Format("Jan 2")
		default:
			out[i] = t.Format("Jan 2")
		}
	}
	return out
}

// Hours is the span of one bucket in hours (for rates).
func (b Buckets) Hours() float64 {
	if b.Step > 0 {
		return b.Step.Hours()
	}
	return float64(24 * b.Days)
}

// hourKey is the UTC hour prefix of the store's time format ("2026-10-14T20"), for the activity table.
func hourKey(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

// parseHour reads an activity hour key.
func parseHour(s string) time.Time {
	t, _ := time.Parse("2006-01-02T15", s)
	return t
}
