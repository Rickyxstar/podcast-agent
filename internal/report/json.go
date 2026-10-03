package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// WriteJSON writes r as indented JSON followed by a newline. Keys follow the
// struct field order, so the same report always produces the same bytes. Nil
// slices are written as [] rather than null, and HTML characters are left
// unescaped so text reads as it was spoken.
func (r *Report) WriteJSON(w io.Writer) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r.withEmptySlices()); err != nil {
		return fmt.Errorf("report: encode json: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("report: write json: %w", err)
	}
	return nil
}

// withEmptySlices returns a copy of r with every nil slice replaced by an
// empty one. r is not modified.
func (r *Report) withEmptySlices() *Report {
	c := *r
	c.Episode.Guests = orEmpty(c.Episode.Guests)
	c.Takeaways = orEmpty(c.Takeaways)
	c.Quotes = orEmpty(c.Quotes)
	c.Topics = orEmpty(c.Topics)
	c.Run.Warnings = orEmpty(c.Run.Warnings)

	c.FactCheck.Claims = make([]Claim, len(r.FactCheck.Claims))
	for i, cl := range r.FactCheck.Claims {
		cl.Evidence = orEmpty(cl.Evidence)
		c.FactCheck.Claims[i] = cl
	}
	return &c
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
