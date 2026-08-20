package logger

import (
	"context"
	"log/slog"
	"time"
)

// Entry is a captured structured log record.
type Entry struct {
	// Seq is a monotonic identifier used by the UI to deduplicate snapshot
	// and live-streamed entries. It never resets, even after a buffer clear.
	Seq     uint64         `json:"seq"`
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// captureHandler serializes records via an inner handler and additionally
// records every emitted log entry into a ring buffer so the UI can show
// application logs. The inner handler owns all formatting/writing.
type captureHandler struct {
	inner slog.Handler
	buff  *Buffer
	attrs []slog.Attr
	group string
}

var _ slog.Handler = (*captureHandler)(nil)

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *captureHandler) Handle(ctx context.Context, record slog.Record) error {
	// Seq 0 lets Append assign the sequence number under its own lock, so
	// buffer order always matches sequence order. Assigning here instead
	// would let a concurrent Handle insert a higher seq first, corrupting
	// the ordering invariant the UI's dedup relies on.
	h.buff.Append(Entry{
		Seq:     0,
		Time:    record.Time,
		Level:   record.Level.String(),
		Message: record.Message,
		Attrs:   entryAttrs(h.attrs, h.group, record),
	})
	return h.inner.Handle(ctx, record)
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{
		inner: h.inner.WithAttrs(attrs),
		buff:  h.buff,
		attrs: append(append([]slog.Attr{}, h.attrs...), attrs...),
		group: h.group,
	}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	g := h.group
	if g != "" {
		g += "."
	}
	g += name
	return &captureHandler{
		inner: h.inner.WithGroup(name),
		buff:  h.buff,
		attrs: h.attrs,
		group: g,
	}
}

// entryAttrs flattens handler attrs, group context, and the record's own
// attrs into a single map. Group keys are prefixed with dot-separated names.
func entryAttrs(over []slog.Attr, group string, record slog.Record) map[string]any {
	out := make(map[string]any)
	var collect func(attrs []slog.Attr, prefix string)
	collect = func(attrs []slog.Attr, prefix string) {
		for _, a := range attrs {
			key := a.Key
			if prefix != "" {
				key = prefix + "." + a.Key
			}
			if a.Value.Kind() == slog.KindGroup {
				collect(a.Value.Group(), key)
				continue
			}
			out[key] = a.Value.Any()
		}
	}
	collect(over, group)
	record.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if group != "" {
			key = group + "." + a.Key
		}
		if a.Value.Kind() == slog.KindGroup {
			collect(a.Value.Group(), key)
			return true
		}
		out[key] = a.Value.Any()
		return true
	})
	if len(out) == 0 {
		return nil
	}
	return out
}
