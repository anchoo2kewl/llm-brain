// Package charts renders small charts as inline SVG on the server: no JavaScript, no build step.
//
// Every chart is a template.HTML string built from escaped text. Colours are never written into the markup:
// a series gets the class s0..s7 and its marks use fill/stroke="currentColor", so the page's CSS (and its
// dark-mode variables) decides the palette. Hover tooltips are SVG <title> elements; each chart carries a
// <title>/<desc> pair and role="img" for screen readers. Charts scale with their container (viewBox plus
// width:100% in CSS).
package charts

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"sort"
	"strings"
)

// Palette is the number of categorical series classes (s0..s7) the stylesheet defines. A ninth series is
// folded into "Other" by the caller (see Fold).
const Palette = 8

// Series is one named run of values, one per x position.
type Series struct {
	Name   string
	Values []float64
	// Class overrides the categorical slot ("s3", "ok", "bad", ...). Empty: s<index>.
	Class string
}

// Opts are the options shared by the x/y charts.
type Opts struct {
	ID      string   // unique on the page; used for aria ids
	Title   string   // accessible name (also the first line of every tooltip)
	Desc    string   // accessible description
	XLabels []string // one per x position: the tooltip heading; a few are drawn on the axis
	Height  int      // viewBox height (default 180)
	Format  func(float64) string
	Stacked bool
	// Lines draws unfilled lines instead of areas (Area only).
	Lines bool
	// Max fixes the y-axis top (e.g. 100 for percentages); 0 picks a round number above the data.
	Max float64
}

const (
	width   = 640
	padL    = 46
	padR    = 10
	padT    = 10
	padB    = 24
	maxTick = 6
)

func (o *Opts) defaults() {
	if o.Height <= 0 {
		o.Height = 180
	}
	if o.Format == nil {
		o.Format = Compact
	}
	if o.ID == "" {
		o.ID = "chart"
	}
}

func esc(s string) string { return html.EscapeString(s) }

// d1 prints one decimal, a trailing ".0" trimmed (4.1, 12).
func d1(v float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") }

// f1 prints up to two decimals, trailing zeros trimmed (2.5, 0.25, 12).
func f1(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

func class(s Series, i int) string {
	if s.Class != "" {
		return s.Class
	}
	return fmt.Sprintf("s%d", i%Palette)
}

// Compact formats a count: 950, 1.2K, 3.4M, 1.1B.
func Compact(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e9:
		return d1(v/1e9) + "B"
	case a >= 1e6:
		return d1(v/1e6) + "M"
	case a >= 1e3:
		return d1(v/1e3) + "K"
	case a == math.Trunc(a):
		return fmt.Sprintf("%.0f", v)
	}
	return f1(v)
}

// Dollars formats an estimate: $0.42, $12, $1.2K.
func Dollars(v float64) string {
	switch {
	case v >= 1000:
		return "$" + Compact(v)
	case v >= 10:
		return fmt.Sprintf("$%.0f", v)
	case v == 0:
		return "$0"
	}
	return fmt.Sprintf("$%.2f", v)
}

// Percent formats 0..100.
func Percent(v float64) string { return fmt.Sprintf("%.0f%%", v) }

// Millis formats a latency.
func Millis(v float64) string {
	if v >= 1000 {
		return f1(v/1000) + "s"
	}
	return fmt.Sprintf("%.0fms", v)
}

// niceMax returns a round axis top above v whose quarter (the tick step) is also round.
func niceMax(v float64) float64 {
	if v <= 0 {
		return 1
	}
	step := v / 4
	p := math.Pow(10, math.Floor(math.Log10(step)))
	for _, m := range []float64{1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10} {
		if step <= m*p*(1+1e-9) {
			return 4 * m * p
		}
	}
	return 40 * p
}

func head(b *strings.Builder, o Opts, h int, extra string) {
	fmt.Fprintf(b, `<svg class="chart%s" viewBox="0 0 %d %d" role="img" aria-labelledby="%s-t %s-d">`, extra, width, h, esc(o.ID), esc(o.ID))
	fmt.Fprintf(b, `<title id="%s-t">%s</title><desc id="%s-d">%s</desc>`, esc(o.ID), esc(o.Title), esc(o.ID), esc(o.Desc))
}

// columns returns the number of x positions across all series.
func columns(series []Series) int {
	n := 0
	for _, s := range series {
		n = max(n, len(s.Values))
	}
	return n
}

func val(s Series, i int) float64 {
	if i < len(s.Values) {
		return s.Values[i]
	}
	return 0
}

// yAxis draws gridlines and tick labels and returns the y of a value.
func yAxis(b *strings.Builder, o Opts, top float64) func(float64) float64 {
	h := float64(o.Height - padT - padB)
	y := func(v float64) float64 { return float64(padT) + h - v/top*h }
	b.WriteString(`<g class="grid">`)
	for i := 0; i <= 4; i++ {
		v := top * float64(i) / 4
		yy := y(v)
		fmt.Fprintf(b, `<line x1="%d" x2="%d" y1="%.1f" y2="%.1f"/>`, padL, width-padR, yy, yy)
		fmt.Fprintf(b, `<text class="tick" x="%d" y="%.1f" text-anchor="end" dy="4">%s</text>`, padL-6, yy, esc(o.Format(v)))
	}
	b.WriteString(`</g>`)
	return y
}

// xLabels writes up to maxTick evenly spaced x labels.
func xLabels(b *strings.Builder, o Opts, n int, x func(int) float64) {
	if len(o.XLabels) == 0 || n == 0 {
		return
	}
	step := max(1, int(math.Ceil(float64(n)/maxTick)))
	for i := 0; i < n && i < len(o.XLabels); i += step {
		anchor := "middle"
		if i == 0 && n > 1 {
			anchor = "start"
		}
		fmt.Fprintf(b, `<text class="tick" x="%.1f" y="%d" text-anchor="%s">%s</text>`, x(i), o.Height-6, anchor, esc(o.XLabels[i]))
	}
}

// tooltip is the text of one x position: its label, then each series' value.
func tooltip(o Opts, series []Series, i int) string {
	var t strings.Builder
	if i < len(o.XLabels) {
		t.WriteString(o.XLabels[i])
	}
	total := 0.0
	for _, s := range series {
		v := val(s, i)
		total += v
		fmt.Fprintf(&t, "\n%s: %s", s.Name, o.Format(v))
	}
	if o.Stacked && len(series) > 1 {
		fmt.Fprintf(&t, "\nTotal: %s", o.Format(total))
	}
	return t.String()
}

func topOf(o Opts, series []Series, n int) float64 {
	if o.Max > 0 {
		return o.Max
	}
	m := 0.0
	for i := 0; i < n; i++ {
		sum := 0.0
		for _, s := range series {
			v := val(s, i)
			if o.Stacked {
				sum += v
			} else {
				sum = max(sum, v)
			}
		}
		m = max(m, sum)
	}
	return niceMax(m)
}

// Area draws one area (or line) per series over x positions; Stacked piles them up.
func Area(o Opts, series []Series) template.HTML {
	o.defaults()
	n := columns(series)
	var b strings.Builder
	head(&b, o, o.Height, " area")
	if n == 0 {
		return empty(&b, o)
	}
	top := topOf(o, series, n)
	y := yAxis(&b, o, top)
	span := float64(width - padL - padR)
	x := func(i int) float64 {
		if n == 1 {
			return float64(padL) + span/2
		}
		return float64(padL) + span*float64(i)/float64(n-1)
	}
	base := make([]float64, n)
	for si, s := range series {
		cls := class(s, si)
		upper := make([]float64, n)
		for i := 0; i < n; i++ {
			upper[i] = val(s, i)
			if o.Stacked {
				upper[i] += base[i]
			}
		}
		var line strings.Builder
		for i := 0; i < n; i++ {
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&line, "%s%.1f %.1f", cmd, x(i), y(upper[i]))
		}
		fmt.Fprintf(&b, `<g class="%s">`, cls)
		if !o.Lines {
			var area strings.Builder
			area.WriteString(line.String())
			for i := n - 1; i >= 0; i-- {
				lo := 0.0
				if o.Stacked {
					lo = base[i]
				}
				fmt.Fprintf(&area, "L%.1f %.1f", x(i), y(lo))
			}
			area.WriteString("Z")
			fmt.Fprintf(&b, `<path class="wash" d="%s" fill="currentColor"/>`, area.String())
		}
		fmt.Fprintf(&b, `<path class="line" d="%s" fill="none" stroke="currentColor"/>`, line.String())
		if n == 1 {
			fmt.Fprintf(&b, `<circle class="dot" cx="%.1f" cy="%.1f" r="4" fill="currentColor"/>`, x(0), y(upper[0]))
		}
		b.WriteString(`</g>`)
		if o.Stacked {
			base = upper
		}
	}
	xLabels(&b, o, n, x)
	// Hover columns: a full-height target per x position whose <title> lists every series.
	colW := span / float64(max(1, n-1))
	if n == 1 {
		colW = span
	}
	b.WriteString(`<g class="hits">`)
	for i := 0; i < n; i++ {
		x0 := max(float64(padL), x(i)-colW/2)
		x1 := min(float64(width-padR), x(i)+colW/2)
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="%d"><title>%s</title></rect>`,
			x0, padT, x1-x0, o.Height-padT-padB, esc(tooltip(o, series, i)))
	}
	b.WriteString(`</g></svg>`)
	return template.HTML(b.String())
}

// Bars draws vertical bars per x position, stacked when there are several series.
func Bars(o Opts, series []Series) template.HTML {
	o.defaults()
	o.Stacked = true
	n := columns(series)
	var b strings.Builder
	head(&b, o, o.Height, " bars")
	if n == 0 {
		return empty(&b, o)
	}
	top := topOf(o, series, n)
	y := yAxis(&b, o, top)
	slot := float64(width-padL-padR) / float64(n)
	bw := min(24, slot*0.7)
	x := func(i int) float64 { return float64(padL) + slot*(float64(i)+0.5) }
	for i := 0; i < n; i++ {
		lo := 0.0
		fmt.Fprintf(&b, `<g class="col"><title>%s</title>`, esc(tooltip(o, series, i)))
		// Hover target: the whole column, behind the bars.
		fmt.Fprintf(&b, `<rect class="hit" x="%.1f" y="%d" width="%.1f" height="%d"/>`, x(i)-slot/2, padT, slot, o.Height-padT-padB)
		for si, s := range series {
			v := val(s, i)
			if v <= 0 {
				continue
			}
			y0, y1 := y(lo), y(lo+v)
			h := y0 - y1
			if lo > 0 && h > 3 {
				h -= 2 // surface gap between stacked segments
			}
			fmt.Fprintf(&b, `<rect class="%s" x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="2" fill="currentColor"/>`,
				class(s, si), x(i)-bw/2, y1, bw, max(h, 0.8))
			lo += v
		}
		b.WriteString(`</g>`)
	}
	xLabels(&b, o, n, x)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func empty(b *strings.Builder, o Opts) template.HTML {
	fmt.Fprintf(b, `<text class="tick" x="%d" y="%d" text-anchor="middle">No data in this range</text></svg>`, width/2, o.Height/2)
	return template.HTML(b.String())
}

// Bar is one row of a horizontal bar list.
type Bar struct {
	Label string
	Value float64
	Note  string // shown after the value, e.g. "12% errors"
	Href  string // optional link on the label
	Class string // optional series class (default s0)
}

// HBars renders a ranked list of horizontal bars as HTML rows (labels stay real text, so they wrap and stay
// readable at any width); each bar is a tiny SVG with no text.
func HBars(id, title string, bars []Bar, format func(float64) string) template.HTML {
	if format == nil {
		format = Compact
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="hbars" role="list" aria-label="%s" id="%s">`, esc(title), esc(id))
	if len(bars) == 0 {
		b.WriteString(`<p class="mut small">No data in this range</p></div>`)
		return template.HTML(b.String())
	}
	top := 0.0
	for _, x := range bars {
		top = max(top, x.Value)
	}
	if top <= 0 {
		top = 1
	}
	for _, x := range bars {
		cls := x.Class
		if cls == "" {
			cls = "s0"
		}
		label := esc(x.Label)
		if x.Href != "" {
			label = fmt.Sprintf(`<a href="%s">%s</a>`, esc(x.Href), label)
		}
		val := esc(format(x.Value))
		if x.Note != "" {
			val += ` <span class="mut">` + esc(x.Note) + `</span>`
		}
		fmt.Fprintf(&b, `<div class="hb" role="listitem" title="%s: %s"><span class="hb-l">%s</span><span class="hb-v">%s</span>`,
			esc(x.Label), esc(format(x.Value)), label, val)
		fmt.Fprintf(&b, `<svg class="hb-b %s" viewBox="0 0 100 6" preserveAspectRatio="none" aria-hidden="true"><rect class="track" width="100" height="6" rx="3"/><rect width="%.2f" height="6" rx="3" fill="currentColor"/></svg></div>`,
			esc(cls), max(0.8, x.Value/top*100))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// Sparkline is a small trend line with the last point marked. It has no axes; title is its tooltip.
func Sparkline(values []float64, title, cls string) template.HTML {
	if cls == "" {
		cls = "s0"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="spark %s" viewBox="0 0 100 24" preserveAspectRatio="none" role="img" aria-label="%s"><title>%s</title>`,
		esc(cls), esc(title), esc(title))
	if len(values) == 0 {
		b.WriteString(`</svg>`)
		return template.HTML(b.String())
	}
	top := 0.0
	for _, v := range values {
		top = max(top, v)
	}
	if top <= 0 {
		top = 1
	}
	n := len(values)
	x := func(i int) float64 {
		if n == 1 {
			return 50
		}
		return 1 + 98*float64(i)/float64(n-1)
	}
	y := func(v float64) float64 { return 22 - v/top*20 }
	var d, a strings.Builder
	for i, v := range values {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&d, "%s%.2f %.2f", cmd, x(i), y(v))
	}
	a.WriteString(d.String())
	fmt.Fprintf(&a, "L%.2f 24L%.2f 24Z", x(n-1), x(0))
	fmt.Fprintf(&b, `<path class="wash" d="%s" fill="currentColor"/><path class="line" d="%s" fill="none" stroke="currentColor" vector-effect="non-scaling-stroke"/></svg>`,
		a.String(), d.String())
	return template.HTML(b.String())
}

// Slice is one part of a donut.
type Slice struct {
	Label string
	Value float64
	Class string
}

// Donut draws parts of a whole with the total (or center text) in the middle.
func Donut(id, title string, slices []Slice, center string, format func(float64) string) template.HTML {
	if format == nil {
		format = Compact
	}
	total := 0.0
	for _, s := range slices {
		total += max(0, s.Value)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="donut" viewBox="0 0 120 120" role="img" aria-labelledby="%s-t"><title id="%s-t">%s</title>`, esc(id), esc(id), esc(title))
	const r, cx, cy = 46.0, 60.0, 60.0
	fmt.Fprintf(&b, `<circle class="track" cx="60" cy="60" r="%.0f" fill="none" stroke-width="16"/>`, r)
	if total > 0 {
		start := -math.Pi / 2
		for i, s := range slices {
			if s.Value <= 0 {
				continue
			}
			frac := s.Value / total
			end := start + frac*2*math.Pi
			cls := s.Class
			if cls == "" {
				cls = fmt.Sprintf("s%d", i%Palette)
			}
			tip := fmt.Sprintf("%s: %s (%.0f%%)", s.Label, format(s.Value), frac*100)
			if frac >= 0.9999 {
				fmt.Fprintf(&b, `<circle class="%s" cx="60" cy="60" r="%.0f" fill="none" stroke="currentColor" stroke-width="16"><title>%s</title></circle>`, cls, r, esc(tip))
				break
			}
			gap := min(0.02, frac*math.Pi) // a sliver of surface between slices
			a0, a1 := start+gap, end-gap
			large := 0
			if a1-a0 > math.Pi {
				large = 1
			}
			fmt.Fprintf(&b, `<path class="%s" d="M%.2f %.2fA%.0f %.0f 0 %d 1 %.2f %.2f" fill="none" stroke="currentColor" stroke-width="16"><title>%s</title></path>`,
				cls, cx+r*math.Cos(a0), cy+r*math.Sin(a0), r, r, large, cx+r*math.Cos(a1), cy+r*math.Sin(a1), esc(tip))
			start = end
		}
	}
	if center == "" {
		center = format(total)
	}
	fmt.Fprintf(&b, `<text class="big" x="60" y="64" text-anchor="middle">%s</text></svg>`, esc(center))
	return template.HTML(b.String())
}

// Heatmap draws a grid of cells (rows × cols) shaded by value on one hue.
func Heatmap(o Opts, rows, cols []string, cells [][]float64) template.HTML {
	o.defaults()
	const left, top, cw, ch, gap = 40, 6, 24, 22, 2
	nc := len(cols)
	w := left + nc*cw + 4
	h := top + len(rows)*ch + 20
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart heat" viewBox="0 0 %d %d" role="img" aria-labelledby="%s-t %s-d">`, w, h, esc(o.ID), esc(o.ID))
	fmt.Fprintf(&b, `<title id="%s-t">%s</title><desc id="%s-d">%s</desc>`, esc(o.ID), esc(o.Title), esc(o.ID), esc(o.Desc))
	peak := 0.0
	for _, r := range cells {
		for _, v := range r {
			peak = max(peak, v)
		}
	}
	for ri, rl := range rows {
		y := top + ri*ch
		fmt.Fprintf(&b, `<text class="tick" x="%d" y="%d" text-anchor="end">%s</text>`, left-6, y+ch/2+4, esc(rl))
		for ci := range cols {
			v := 0.0
			if ri < len(cells) && ci < len(cells[ri]) {
				v = cells[ri][ci]
			}
			cls := "cell"
			op := 1.0
			if v <= 0 || peak <= 0 {
				cls = "cell zero"
			} else {
				op = 0.18 + 0.82*math.Sqrt(v/peak)
			}
			fmt.Fprintf(&b, `<rect class="%s" x="%d" y="%d" width="%d" height="%d" rx="3" fill="currentColor" fill-opacity="%.2f"><title>%s %s: %s</title></rect>`,
				cls, left+ci*cw, y, cw-gap, ch-gap, op, esc(rl), esc(cols[ci]), esc(o.Format(v)))
		}
	}
	for ci, cl := range cols {
		if ci%3 == 0 {
			fmt.Fprintf(&b, `<text class="tick" x="%d" y="%d" text-anchor="middle">%s</text>`, left+ci*cw+cw/2, h-4, esc(cl))
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// Gauge is a half-circle meter for a percentage used (0..100). Its colour is the status of the value:
// ok under warnAt, warn under badAt, bad above.
func Gauge(id, title string, used float64, center, sub string) template.HTML {
	used = max(0, min(100, used))
	cls := "ok"
	switch {
	case used >= 90:
		cls = "bad"
	case used >= 70:
		cls = "warn"
	}
	const r, cx, cy = 50.0, 60.0, 60.0
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="gauge" viewBox="0 0 120 66" role="img" aria-labelledby="%s-t"><title id="%s-t">%s</title>`, esc(id), esc(id), esc(title))
	fmt.Fprintf(&b, `<path class="track" d="M10 60A50 50 0 0 1 110 60" fill="none" stroke-width="10" stroke-linecap="round"/>`)
	if used > 0 {
		a := math.Pi * (1 - used/100) // from 180° (left) toward 0° (right)
		fmt.Fprintf(&b, `<path class="g-%s" d="M10 60A50 50 0 0 1 %.2f %.2f" fill="none" stroke="currentColor" stroke-width="10" stroke-linecap="round"/>`,
			cls, cx+r*math.Cos(a), cy-r*math.Sin(a))
	}
	fmt.Fprintf(&b, `<text class="gv" x="60" y="58" text-anchor="middle">%s</text>`, esc(center))
	b.WriteString(`</svg>`)
	if sub != "" {
		fmt.Fprintf(&b, `<p class="gauge-sub">%s</p>`, esc(sub))
	}
	return template.HTML(b.String())
}

// LegendItem names one series in an HTML legend.
type LegendItem struct {
	Name  string
	Class string
	Value string // optional, e.g. the series total
}

// Legend renders an HTML legend: a colour swatch beside each name (text stays in the text colour).
func Legend(items []LegendItem) template.HTML {
	if len(items) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<ul class="legend">`)
	for i, it := range items {
		cls := it.Class
		if cls == "" {
			cls = fmt.Sprintf("s%d", i%Palette)
		}
		fmt.Fprintf(&b, `<li><span class="sw %s" aria-hidden="true"></span>%s`, esc(cls), esc(it.Name))
		if it.Value != "" {
			fmt.Fprintf(&b, ` <span class="mut">%s</span>`, esc(it.Value))
		}
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ul>`)
	return template.HTML(b.String())
}

// LegendOf builds a legend from series, with each series' total.
func LegendOf(series []Series, format func(float64) string) template.HTML {
	if format == nil {
		format = Compact
	}
	items := make([]LegendItem, len(series))
	for i, s := range series {
		t := 0.0
		for _, v := range s.Values {
			t += v
		}
		items[i] = LegendItem{Name: s.Name, Class: class(s, i), Value: format(t)}
	}
	return Legend(items)
}

// Fold keeps the keep-1 largest series (by total) and sums the rest into "Other", so a chart never needs a
// ninth colour. Kept series stay in their input order, so a series keeps its colour slot when others come and
// go; callers pass a stable order (e.g. by name).
func Fold(series []Series, keep int) []Series {
	if keep < 1 {
		keep = Palette
	}
	if len(series) <= keep {
		return series
	}
	tot := make([]float64, len(series))
	idx := make([]int, len(series))
	for i, s := range series {
		idx[i] = i
		for _, v := range s.Values {
			tot[i] += v
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return tot[idx[a]] > tot[idx[b]] })
	kept := map[int]bool{}
	for _, i := range idx[:keep-1] {
		kept[i] = true
	}
	var out []Series
	other := Series{Name: "Other", Values: make([]float64, columns(series))}
	for i, s := range series {
		if kept[i] {
			out = append(out, s)
			continue
		}
		for j, v := range s.Values {
			other.Values[j] += v
		}
	}
	return append(out, other)
}
