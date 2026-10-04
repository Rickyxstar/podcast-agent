package agent

import "testing"

func TestPriceFor(t *testing.T) {
	opus := prices["claude-opus-5-5"]
	tests := []struct {
		model  string
		want   Price
		wantOK bool
	}{
		{"claude-opus-5-5", opus, true},
		{"anthropic.claude-opus-5-5", opus, true},
		{"us.anthropic.claude-opus-5-5", opus, true},
		{"claude-haiku-4-5", Price{Input: 1, Output: 5, CacheRead: 0.10, CacheWrite: 1.25}, true},
		{"gpt-5.6-sol", Price{Input: 4, Output: 20, CacheRead: 0.40, CacheWrite: 5}, true},
		{"qwen2.5:7b", Price{}, false},
		{"", Price{}, false},
	}
	for _, tt := range tests {
		got, ok := PriceFor(tt.model)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("PriceFor(%q) = %+v, %v; want %+v, %v", tt.model, got, ok, tt.want, tt.wantOK)
		}
	}
}
