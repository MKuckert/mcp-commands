package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
)

// logHandler is a compact slog.Handler that renders each record on one line as
//
//	<LEVEL>@<HH:mm:ss> <message> [ | key=value …]
//
// The message renders unquoted; attribute values containing whitespace are
// double-quoted (see renderString). A ` | ` separator joins the message and the
// attribute list only when at least one attribute renders — a record with no
// attributes ends right after the message.
//
// e.g. `WARN@18:02:11 ignoring invalid Timeout | file=/path/x.sh error="invalid timeout \"abc\""`.
//
// The handler is stateless apart from the immutable accumulated attrs and
// group (WithAttrs/WithGroup return new values, never mutating the receiver),
// so concurrent Handle calls are safe without a mutex.
type logHandler struct {
	out      io.Writer
	minLevel slog.Level
	group    string
	attrs    []slog.Attr
}

// newLogHandler builds a handler writing to w, emitting records at or above
// minLevel (replaces slog.HandlerOptions.Level for this format).
func newLogHandler(w io.Writer, minLevel slog.Level) *logHandler {
	return &logHandler{out: w, minLevel: minLevel}
}

// Enabled reports whether a record at the given level would be written.
func (h *logHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel
}

// Handle renders the record as one line followed by a newline.
func (h *logHandler) Handle(_ context.Context, rec slog.Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s@%s %s", rec.Level.String(), rec.Time.Format("15:04:05"), rec.Message)
	first := true
	for _, attr := range h.attrs {
		writeAttr(&b, &first, h.group, attr)
	}
	rec.Attrs(func(attr slog.Attr) bool {
		writeAttr(&b, &first, h.group, attr)
		return true
	})
	b.WriteByte('\n')
	_, err := h.out.Write([]byte(b.String()))
	return err
}

// WithAttrs returns a new handler carrying the accumulated attrs plus attrs.
func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &logHandler{out: h.out, minLevel: h.minLevel, group: h.group, attrs: merged}
}

// WithGroup returns a new handler prefixing all subsequent keys with name.
func (h *logHandler) WithGroup(name string) slog.Handler {
	group := name
	if h.group != "" {
		group = h.group + "." + name
	}
	return &logHandler{out: h.out, minLevel: h.minLevel, group: group, attrs: h.attrs}
}

// writeAttr appends one `key=value` (with the group prefix applied); the first
// attribute is joined to the message with a ` | ` separator, each subsequent
// one with a single space. Nested groups flatten to `group.key`. Keys are never
// quoted. An empty group writes nothing, leaving `*first` untouched.
func writeAttr(b *strings.Builder, first *bool, group string, attr slog.Attr) {
	key := attr.Key
	if group != "" {
		key = group + "." + key
	}
	if attr.Value.Kind() == slog.KindGroup {
		for _, sub := range attr.Value.Group() {
			writeAttr(b, first, key, sub)
		}
		return
	}
	if *first {
		b.WriteString(" | ")
		*first = false
	} else {
		b.WriteByte(' ')
	}
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(renderString(attr.Value.String()))
}

// renderString double-quotes attribute values containing whitespace; all other
// values render verbatim (slog Value.String already formats numbers, times, etc.).
func renderString(s string) string {
	if strings.ContainsAny(s, " \t\n") {
		return strconv.Quote(s)
	}
	return s
}
