package insights

import (
	"sort"
	"strings"
)

// Price is an API list price in US dollars per million tokens. The hub uses it only to estimate what the
// tokens served by subscriptions would have cost on the pay-as-you-go API: an estimate, not a bill.
type Price struct {
	Input      float64 `json:"input"`       // uncached input
	Output     float64 `json:"output"`      // output (thinking included)
	CacheRead  float64 `json:"cache_read"`  // cached input read
	CacheWrite float64 `json:"cache_write"` // input written to the cache (5-minute TTL)
}

// Prices maps a model-name prefix to its price; the longest matching prefix wins. Edit this table to match
// your providers' current rates (cache writes are 1.25x input and cache reads 0.1x input on Claude; OpenAI
// models get a generic GPT-class rate with cached input at 0.1x). Unknown models are not priced.
var Prices = map[string]Price{
	"claude-fable":    {10, 50, 0.25, 12.5},
	"claude-mythos":   {10, 50, 0.25, 12.5},
	"claude-opus-5-5": {4, 20, 0.20, 5},
	"claude-opus-5":   {5, 25, 0.50, 6.25},
	"claude-opus-4":   {5, 25, 0.50, 6.25},
	"claude-opus":     {4, 20, 0.20, 5},
	"claude-sonnet-5": {2, 10, 0.20, 2.5},
	"claude-sonnet-4": {3, 15, 0.30, 3.75},
	"claude-sonnet":   {2, 10, 0.20, 2.5},
	"claude-haiku-5":  {0.10, 0.50, 0.01, 0.125},
	"claude-haiku-4":  {1, 5, 0.10, 1.25},
	"claude-haiku":    {0.10, 0.50, 0.01, 0.125},
	"gpt-":            {1.25, 10, 0.125, 0},
	"codex-":          {1.25, 10, 0.125, 0},
	"o3":              {2, 8, 0.5, 0},
	"o4-mini":         {1.1, 4.4, 0.275, 0},
}

// PriceFor returns the price for a model and whether one is known.
func PriceFor(model string) (Price, bool) {
	m := strings.ToLower(model)
	best := ""
	for k := range Prices {
		if strings.HasPrefix(m, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return Price{}, false
	}
	return Prices[best], true
}

// Tokens is a token count split the way prices are.
type Tokens struct {
	Input      int64 `json:"input"` // uncached input
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

func (t *Tokens) Add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// Cost is the estimated API-equivalent dollar value of t on model (0 when the model has no price).
func Cost(model string, t Tokens) float64 {
	p, ok := PriceFor(model)
	if !ok {
		return 0
	}
	return (float64(t.Input)*p.Input + float64(t.Output)*p.Output + float64(t.CacheRead)*p.CacheRead +
		float64(t.CacheWrite)*p.CacheWrite) / 1e6
}

// GatewayTokens splits a gateway request's counts. Claude-style usage reports cached input apart from input
// and folds cache writes into the total; OpenAI-style usage counts cached input inside input.
func GatewayTokens(model string, input, output, cached, total int64) Tokens {
	if strings.HasPrefix(strings.ToLower(model), "claude") {
		return Tokens{Input: input, Output: output, CacheRead: cached, CacheWrite: max(0, total-input-output-cached)}
	}
	return Tokens{Input: max(0, input-cached), Output: output, CacheRead: cached}
}

// PriceRow is one line of the price table as shown on the page.
type PriceRow struct {
	Prefix string `json:"prefix"`
	Price
}

func PriceTable() []PriceRow {
	out := make([]PriceRow, 0, len(Prices))
	for k, p := range Prices {
		out = append(out, PriceRow{k, p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}
