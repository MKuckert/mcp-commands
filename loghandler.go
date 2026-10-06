package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
)

// messageEscaper renders newline and carriage return in the record message as
// the two-character literals `\n` / `\r`, preserving the one-line-per-record
// invariant.
var messageEscaper = strings.NewReplacer("\n", `\n`, "\r", `\r`)

// logHandler is a compact slog.Handler that renders each record on one line as
//
//	<LEVEL>@<HH:mm:ss> <message> [ | key=value …]
//
// The message renders unquoted; any `\n` / `\r` in it is escaped as the
// two-character literals `\n` / `\r` so every record stays on one physical
// line. Attribute values containing whitespace are double-quoted (see
// renderString). A ` | ` separator joins the message and the attribute list
// only when at least one attribute renders — a record with no attributes ends
// right after the message.
//
// Group contract: WithAttrs bakes the handler's current group prefix into each
// new attribute's key (a group-valued attribute gets its *name* prefixed,
// keeping its children unprefixed), and WithGroup("") is a no-op. Handle
// therefore applies h.group only to the record's own attributes, never to the
// accumulated ones (whose keys are already baked in):
// `logger.With("a", 1).WithGroup("g").Info("m", "b", 2)` renders
// `m | a=1 g.b=2`.
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
	fmt.Fprintf(&b, "%s@%s %s", rec.Level.String(), rec.Time.Format("15:04:05"), messageEscaper.Replace(rec.Message))
	first := true
	// h.attrs carry baked-in keys, so no group prefix is applied; the record's
	// own attrs are the only ones h.group prefixes.
	for _, attr := range h.attrs {
		writeAttr(&b, &first, "", attr)
	}
	rec.Attrs(func(attr slog.Attr) bool {
		writeAttr(&b, &first, h.group, attr)
		return true
	})
	b.WriteByte('\n')
	_, err := h.out.Write([]byte(b.String()))
	return err
}

// WithAttrs returns a new handler carrying the accumulated attrs plus attrs,
// baking the current group prefix into each new attr's key (a group-valued
// attr gets its name prefixed, keeping its children intact).
func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	for _, attr := range attrs {
		// A group-valued attr gets its name prefixed; its children keep
		// their own keys.
		if h.group != "" {
			attr.Key = h.group + "." + attr.Key
		}
		merged = append(merged, attr)
	}
	return &logHandler{out: h.out, minLevel: h.minLevel, group: h.group, attrs: merged}
}

// WithGroup returns a new handler prefixing all subsequent keys with name.
// An empty name is a no-op: it returns the equivalent handler without
// appending a dot.
func (h *logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
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
	v := attr.Value.Resolve()
	key := attr.Key
	if group != "" {
		key = group + "." + key
	}
	if v.Kind() == slog.KindGroup {
		for _, sub := range v.Group() {
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
	b.WriteString(renderString(v.String()))
}

// renderString double-quotes attribute values containing whitespace; all other
// values render verbatim (slog Value.String already formats numbers, times, etc.).
func renderString(s string) string {
	if strings.ContainsAny(s, " \t\n\r") {
		return strconv.Quote(s)
	}
	return s
}
