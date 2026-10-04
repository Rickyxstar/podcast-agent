// Package trace records the agent's reasoning steps for one episode: the
// plan, each model and tool call, the verdicts and the validation result.
//
// Events go to a Sink carried in the context, in the manner of
// net/http/httptrace, so one Agent can serve concurrent runs that each
// collect their own trace. A Recorder keeps a run's events for trace.jsonl;
// Pretty prints them as they happen for a human watching the console.
package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Event is one trace event, such as plan, llm_call, tool_call, tool_result,
// verdict, validation or done.
type Event struct {
	Time    time.Time
	TraceID string
	Episode string
	Type    string
	// Attrs are the event's fields, e.g. stage, duration_ms and tokens.
	Attrs []slog.Attr
}

// Value returns the value of the attribute named key, or the zero Value if
// e has none.
func (e Event) Value(key string) slog.Value {
	for _, a := range e.Attrs {
		if a.Key == key {
			return a.Value.Resolve()
		}
	}
	return slog.Value{}
}

// MarshalJSON writes e as one flat object, with ts, trace_id, episode and
// event first and then the attributes in the order they were given.
func (e Event) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	b.WriteByte('{')
	fields := append([]slog.Attr{
		slog.Time("ts", e.Time),
		slog.String("trace_id", e.TraceID),
		slog.String("episode", e.Episode),
		slog.String("event", e.Type),
	}, e.Attrs...)
	for i, a := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		// Encode appends a newline, which JSON allows between tokens.
		if err := enc.Encode(a.Key); err != nil {
			return nil, err
		}
		b.WriteByte(':')
		if err := enc.Encode(jsonValue(a.Value)); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func jsonValue(v slog.Value) any {
	v = v.Resolve()
	if v.Kind() != slog.KindGroup {
		return v.Any()
	}
	m := make(map[string]any, len(v.Group()))
	for _, a := range v.Group() {
		m[a.Key] = jsonValue(a.Value)
	}
	return m
}

// Sink receives trace events. Implementations must be safe for concurrent
// use, since an episode's stages run in parallel.
type Sink interface {
	Emit(Event)
}

type sinkKey struct{}

// WithSink returns a copy of ctx whose trace events go to s.
func WithSink(ctx context.Context, s Sink) context.Context {
	return context.WithValue(ctx, sinkKey{}, s)
}

// FromContext returns the Sink set by WithSink, or nil if there is none.
func FromContext(ctx context.Context) Sink {
	s, _ := ctx.Value(sinkKey{}).(Sink)
	return s
}

// Tee returns a Sink that sends each event to every one of sinks.
func Tee(sinks ...Sink) Sink { return tee(sinks) }

type tee []Sink

func (t tee) Emit(e Event) {
	for _, s := range t {
		s.Emit(e)
	}
}

// Recorder keeps every event it receives, in order, for trace.jsonl.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

// Emit records e.
func (r *Recorder) Emit(e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

// Events returns a copy of the events recorded so far.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// WriteJSONL writes the recorded events to w as JSON Lines, one event per
// line.
func (r *Recorder) WriteJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, e := range r.Events() {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}
