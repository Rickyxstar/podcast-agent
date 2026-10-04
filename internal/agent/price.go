package agent

import "strings"

// prices are first-party API list rates in USD per million tokens, keyed by
// the bare model ID. Bedrock is billed separately by AWS, so its costs are an
// estimate.
var prices = map[string]Price{
	// Anthropic. CacheWrite is the 5-minute TTL rate (1.25x input).
	"claude-fable-5-1":  {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.50},
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite: 5},
	"claude-opus-4-8":   {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite: 2.50},
	"claude-haiku-4-5":  {Input: 1, Output: 5, CacheRead: 0.10, CacheWrite: 1.25},

	// OpenAI. Models before gpt-5.6 don't bill cache writes separately, so
	// CacheWrite is the plain input rate.
	"gpt-6-astra":   {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.50},
	"gpt-6.1-sol":   {Input: 2, Output: 10, CacheRead: 0.10, CacheWrite: 2.50},
	"gpt-6-sol":     {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite: 2.50},
	"gpt-6-luna":    {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125},
	"gpt-5.6-sol":   {Input: 4, Output: 20, CacheRead: 0.40, CacheWrite: 5},
	"gpt-5.6-terra": {Input: 2, Output: 12, CacheRead: 0.20, CacheWrite: 2.50},
	"gpt-5.6-luna":  {Input: 0.20, Output: 1.20, CacheRead: 0.02, CacheWrite: 0.25},
	"gpt-5.5":       {Input: 5, Output: 30, CacheRead: 0.50, CacheWrite: 5},
	"gpt-5.4":       {Input: 2.50, Output: 15, CacheRead: 0.25, CacheWrite: 2.50},
}

// PriceFor returns the price of model, accepting Bedrock IDs such as
// "anthropic.claude-opus-5-5" or "us.anthropic.claude-opus-5-5". It reports
// false for models not in the table, including local Ollama models.
func PriceFor(model string) (Price, bool) {
	if i := strings.LastIndex(model, "anthropic."); i >= 0 {
		model = model[i+len("anthropic."):]
	}
	p, ok := prices[model]
	return p, ok
}
