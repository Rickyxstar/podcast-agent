package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrEmptyTranscript is returned when a file decodes but has no transcript segments.
var ErrEmptyTranscript = errors.New("transcript has no segments")

// JSONParser parses transcripts in the episode JSON format (see samples/).
type JSONParser struct{}

var _ Parser = JSONParser{}

func (JSONParser) Parse(name string, r io.Reader) (*Episode, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	var ep Episode
	if err := json.Unmarshal(b, &ep); err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	if len(ep.Transcript) == 0 {
		return nil, fmt.Errorf("%s: %w", name, ErrEmptyTranscript)
	}
	return &ep, nil
}
