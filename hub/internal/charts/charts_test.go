package charts

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// wellFormed parses the markup as XML (every chart is an XML fragment) and counts elements by name.
func wellFormed(t *testing.T, s string) map[string]int {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader("<root>" + s + "</root>"))
	counts := map[string]int{}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return counts
		}
		if err != nil {
			t.Fatalf("not well-formed: %v\n%s", err, s)
		}
		if se, ok := tok.(xml.StartElement); ok {
			counts[se.Name.Local]++
		}
	}
}

func TestAreaStacked(t *testing.T) {
	out := string(Area(Opts{ID: "tok", Title: "Tokens <by> model", XLabels: []string{"Mon", "Tue", "Wed"}, Stacked: true},
		[]Series{{Name: "model-a", Values: []float64{1, 2, 3}}, {Name: "model-b", Values: []float64{4, 0, 1}}}))
	c := wellFormed(t, out)
	if c["svg"] != 1 || c["path"] != 4 { // wash + line per series
		t.Fatalf("want 1 svg and 4 paths, got %v", c)
	}
	if c["rect"] != 3 { // one hover column per x position
		t.Fatalf("want 3 hover columns, got %d", c["rect"])
	}
	for _, want := range []string{`role="img"`, `aria-labelledby="tok-t tok-d"`, `Tokens &lt;by&gt; model`, `class="s0"`, `class="s1"`,
		"Total: 5", `viewBox="0 0 640 180"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "style=") || strings.Contains(out, "#") && strings.Contains(out, "fill=\"#") {
		t.Error("charts must not carry inline styles or literal colours (CSP + theming)")
	}
}

func TestTipsOverrideTooltipHeading(t *testing.T) {
	out := string(Bars(Opts{ID: "t", XLabels: []string{"Mon"}, Tips: []string{"Mon Jan 5 00:00 – 06:00"}}, []Series{{Name: "a", Values: []float64{1}}}))
	if !strings.Contains(out, "<title>Mon Jan 5 00:00 – 06:00\na: 1</title>") || !strings.Contains(out, ">Mon</text>") {
		t.Fatalf("tooltip uses Tips, axis uses XLabels:\n%s", out)
	}
}

func TestAreaLinesAndEmpty(t *testing.T) {
	out := string(Area(Opts{ID: "l", Lines: true, Max: 100, Format: Percent}, []Series{{Name: "a", Values: []float64{10, 90}}}))
	c := wellFormed(t, out)
	if c["path"] != 1 {
		t.Fatalf("lines mode draws only the line, got %d paths", c["path"])
	}
	if !strings.Contains(out, ">100%<") {
		t.Error("fixed max should label the top tick 100%")
	}
	e := string(Area(Opts{ID: "e"}, nil))
	wellFormed(t, e)
	if !strings.Contains(e, "No data") {
		t.Error("empty chart should say so")
	}
}

func TestBarsStack(t *testing.T) {
	out := string(Bars(Opts{ID: "b", XLabels: []string{"a", "b"}}, []Series{
		{Name: "ok", Values: []float64{2, 0}, Class: "ok"}, {Name: "bad", Values: []float64{1, 3}, Class: "bad"}}))
	c := wellFormed(t, out)
	// 2 hover columns + 3 non-zero segments
	if c["rect"] != 5 {
		t.Fatalf("want 5 rects, got %d\n%s", c["rect"], out)
	}
	if !strings.Contains(out, `class="ok"`) || !strings.Contains(out, `class="bad"`) {
		t.Error("explicit classes should be used")
	}
	if c["title"] < 3 {
		t.Error("each column needs a tooltip")
	}
}

func TestHBarsEscapesAndLinks(t *testing.T) {
	out := string(HBars("h", "Top", []Bar{{Label: "<b>x</b>", Value: 10, Href: "/s?a=1&b=2", Note: "5% errors"}, {Label: "y", Value: 5}}, nil))
	wellFormed(t, out)
	if strings.Contains(out, "<b>x</b>") || !strings.Contains(out, "&lt;b&gt;") {
		t.Error("labels must be escaped")
	}
	if !strings.Contains(out, `href="/s?a=1&amp;b=2"`) {
		t.Error("href must be escaped")
	}
	if !strings.Contains(out, `width="50.00"`) {
		t.Errorf("second bar should be half width:\n%s", out)
	}
}

func TestSparkDonutGaugeHeat(t *testing.T) {
	wellFormed(t, string(Sparkline([]float64{1, 3, 2}, "trend", "")))
	wellFormed(t, string(Sparkline(nil, "none", "")))
	d := string(Donut("d", "Share", []Slice{{Label: "personal", Value: 3}, {Label: "work", Value: 1}}, "", nil))
	c := wellFormed(t, d)
	if c["path"] != 2 || !strings.Contains(d, "work: 1 (25%)") {
		t.Fatalf("donut slices: %v\n%s", c, d)
	}
	full := string(Donut("d2", "All", []Slice{{Label: "only", Value: 5}}, "5", nil))
	if c := wellFormed(t, full); c["circle"] != 2 {
		t.Fatalf("a single 100%% slice is a full ring, got %v", c)
	}
	g := string(Gauge("g", "5h window", 95, "5% left", "resets 15:00"))
	if !strings.Contains(g, `<p class="gauge-sub">resets 15:00</p>`) {
		t.Error("gauge sub-label is HTML below the meter")
	}
	wellFormed(t, g)
	if !strings.Contains(g, `class="g-bad"`) {
		t.Error("95% used is bad")
	}
	if !strings.Contains(string(Gauge("g2", "x", 10, "", "")), "g-ok") {
		t.Error("10% used is ok")
	}
	h := string(Heatmap(Opts{ID: "hm", Title: "When"}, []string{"Mon", "Tue"}, []string{"00", "01", "02"}, [][]float64{{0, 1, 4}, {2}}))
	c = wellFormed(t, h)
	if c["rect"] != 6 {
		t.Fatalf("heatmap cells: %d", c["rect"])
	}
	if !strings.Contains(h, `class="cell zero"`) || !strings.Contains(h, `fill-opacity="1.00"`) {
		t.Error("zero cells and the peak cell should be marked")
	}
}

func TestFoldKeepsOrderAndSumsOther(t *testing.T) {
	in := []Series{{Name: "a", Values: []float64{1}}, {Name: "b", Values: []float64{10}}, {Name: "c", Values: []float64{5}}, {Name: "d", Values: []float64{2}}}
	out := Fold(in, 3)
	if len(out) != 3 || out[0].Name != "b" || out[1].Name != "c" || out[2].Name != "Other" || out[2].Values[0] != 3 {
		t.Fatalf("fold: %+v", out)
	}
	if got := Fold(in, 8); len(got) != 4 {
		t.Fatal("no fold under the cap")
	}
}

func TestFormats(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 950: "950", 1200: "1.2K", 1000: "1K", 3_400_000: "3.4M", 2.5: "2.5", 7_500_000: "7.5M", 0.25: "0.25"} {
		if got := Compact(in); got != want {
			t.Errorf("Compact(%v) = %q, want %q", in, got, want)
		}
	}
	if Dollars(0) != "$0" || Dollars(0.4) != "$0.40" || Dollars(12.4) != "$12" || Dollars(1500) != "$1.5K" {
		t.Error("Dollars")
	}
	if Millis(850) != "850ms" || Millis(2500) != "2.5s" {
		t.Error("Millis")
	}
	if niceMax(7) != 8 || niceMax(180) != 200 || niceMax(0) != 1 || niceMax(23) != 24 || niceMax(21.9e6) != 24e6 || niceMax(100) != 100 {
		t.Error("niceMax")
	}
}
