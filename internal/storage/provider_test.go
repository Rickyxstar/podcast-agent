package storage

import (
	"errors"
	"testing"
)

func TestCheckKey(t *testing.T) {
	for _, key := range []string{"a", "incoming/ep001.json", "results/ep001/report.json", ".hidden", "a/..b"} {
		if err := CheckKey(key); err != nil {
			t.Errorf("CheckKey(%q) = %v, want nil", key, err)
		}
	}
	for _, key := range []string{"", ".", "..", "/a", "a/", "a//b", "a/./b", "a/../b", `a\b`, `..\a`} {
		if err := CheckKey(key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("CheckKey(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
}
