package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLogHandlerLevels pins the five app levels (trace, debug, info, warn,
// error) and the one-line, newline-terminated record shape.
func TestLogHandlerLevels(t *testing.T) {
	for _, tc := range []struct {
		level slog.Level
		want  string
	}{
		{levelTrace, "TRACE@"},
		{slog.LevelDebug, "DEBUG@"},
		{slog.LevelInfo, "INFO@"},
		{slog.LevelWarn, "WARN@"},
		{slog.LevelError, "ERROR@"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			var buf bytes.Buffer
			h := newLogHandler(&buf, slog.LevelDebug)
			rec := slog.NewRecord(time.Now(), tc.level, "hello", 0)
			if err := h.Handle(context.Background(), rec); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			out := buf.String()
			if !strings.HasPrefix(out, tc.want) {
				t.Fatalf("record = %q, want prefix %q", out, tc.want)
			}
			if !strings.Contains(out, " hello\n") {
				t.Fatalf("record = %q, want the unquoted message and no trailing separator", out)
			}
			if !strings.HasSuffix(out, "\n") {
				t.Fatalf("record = %q, want trailing newline", out)
			}
		})
	}
}

// TestLogHandlerQuoting pins the quoting rule: the message renders unquoted,
// while any *attribute value* containing whitespace is double-quoted and other
// values render verbatim.
func TestLogHandlerQuoting(t *testing.T) {
	var buf bytes.Buffer
	h := newLogHandler(&buf, slog.LevelDebug)
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "hello world", 0)
	rec.Add(slog.String("quoted", "a b"), slog.String("plain", "x.sh"), slog.Int("n", 42))
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"hello world", `quoted="a b"`, "plain=x.sh", "n=42"} {
		if !strings.Contains(out, want) {
			t.Errorf("record = %q, want it to contain %s", out, want)
		}
	}
	if strings.Contains(out, `"hello world"`) {
		t.Errorf("record = %q, the message must not be quoted", out)
	}
	if strings.Contains(out, `plain="x.sh"`) {
		t.Errorf("record = %q, value without whitespace must not be quoted", out)
	}
}

// TestLogHandlerSeparator pins the conditional ` | ` separator: it is present
// when at least one attribute renders (including WithAttrs-derived ones) and
// absent — with no trailing space — when none do.
func TestLogHandlerSeparator(t *testing.T) {
	var buf bytes.Buffer
	h := newLogHandler(&buf, slog.LevelDebug)

	withAttrs := slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)
	withAttrs.Add(slog.String("k", "v"))
	if err := h.Handle(context.Background(), withAttrs); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "m | k=v") {
		t.Errorf("record = %q, want the ' | ' separator before the first attr", got)
	}

	buf.Reset()
	none := slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)
	if err := h.Handle(context.Background(), none); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := buf.String(); strings.Contains(got, "|") || strings.HasSuffix(got, " \n") {
		t.Errorf("record = %q, no attrs must leave no separator or trailing space", got)
	}

	buf.Reset()
	d := slog.New(h.WithAttrs([]slog.Attr{slog.String("c", "1")}))
	d.Info("m")
	if got := buf.String(); !strings.Contains(got, "m | c=1") {
		t.Errorf("record = %q, want the ' | ' separator before a WithAttrs attr", got)
	}

	// Mixed sources: handler attrs (WithAttrs, group-flattened) and record
	// attrs render in order after a single ' | ' separator.
	buf.Reset()
	mixed := slog.New(h.WithGroup("g").WithAttrs([]slog.Attr{slog.String("c", "1")}))
	mixed.Info("m3", slog.String("k", "v v"))
	got := buf.String()
	if !strings.HasPrefix(got, "INFO@") {
		t.Fatalf("record = %q, want INFO prefix", got)
	}
	if !strings.HasSuffix(got, `m3 | g.c=1 g.k="v v"`+"\n") {
		t.Errorf("record = %q, want exact suffix %q", got, `m3 | g.c=1 g.k="v v"\n`)
	}

	// All attrs are empty groups: nothing renders, so the record ends right
	// after the message — no separator, no trailing space.
	buf.Reset()
	emptyGroups := slog.NewRecord(time.Now(), slog.LevelInfo, "message", 0)
	emptyGroups.Add(slog.Group("g1"), slog.Group("g2"))
	if err := h.Handle(context.Background(), emptyGroups); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got = buf.String()
	if strings.Contains(got, "|") || !strings.HasSuffix(got, " message\n") {
		t.Errorf("record = %q, empty groups must leave no separator and end ' message\n'", got)
	}
}

// TestLogHandlerGroups pins group flattening: nested groups render as
// group.key, and a group name prefixes its attrs.
func TestLogHandlerGroups(t *testing.T) {
	var buf bytes.Buffer
	h := newLogHandler(&buf, slog.LevelDebug)
	rec := slog.NewRecord(time.Now(), slog.LevelWarn, "m", 0)
	rec.Add(slog.Any("flat", "v"),
		slog.Group("g", slog.Any("a", "1"), slog.Any("b", "2 3")),
		slog.Group("outer", slog.Group("inner", slog.Any("k", "x"))))
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"flat=v", "g.a=1", `g.b="2 3"`, "outer.inner.k=x"} {
		if !strings.Contains(out, want) {
			t.Errorf("record = %q, want it to contain %s", out, want)
		}
	}
}

// TestLogHandlerEnabled pins the minimum-level gate: records below the
// configured level are not written; the gate is enforced via the logger.
func TestLogHandlerEnabled(t *testing.T) {
	var buf bytes.Buffer
	h := newLogHandler(&buf, slog.LevelWarn)
	for _, tc := range []struct {
		level slog.Level
		want  bool
	}{
		{slog.LevelDebug, false},
		{slog.LevelInfo, false},
		{slog.LevelWarn, true},
		{slog.LevelError, true},
	} {
		if got := h.Enabled(context.Background(), tc.level); got != tc.want {
			t.Errorf("Enabled(%v) = %v, want %v", tc.level, got, tc.want)
		}
	}
	log := slog.New(h)
	log.Info("filtered")
	if buf.Len() != 0 {
		t.Errorf("info record below min level was written: %q", buf.String())
	}
	log.Warn("kept")
	if !strings.Contains(buf.String(), "WARN@") || !strings.Contains(buf.String(), " kept") {
		t.Errorf("warn record not written: %q", buf.String())
	}
}

// TestLogHandlerWithAttrs pins that attrs accumulated via WithAttrs survive
// on subsequent records, and that the original handler is not mutated (a
// WithAttrs copy and the original share the same sink by design).
func TestLogHandlerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := newLogHandler(&buf, slog.LevelDebug)
	baseLog := slog.New(base)
	derived := slog.New(base.WithAttrs([]slog.Attr{slog.String("ctx", "c1")}))
	derived.Info("record")
	if !strings.Contains(buf.String(), "ctx=c1") {
		t.Fatalf("derived record missing WithAttrs attr: %q", buf.String())
	}
	baseLog.Info("original")
	if strings.Contains(buf.String(), "original ctx=c1") ||
		!strings.Contains(buf.String(), " original\n") {
		t.Fatalf("original handler must not carry the derived attrs: %q", buf.String())
	}
}

// TestLogHandlerLogValuer pins that LogValuer values are resolved before
// kind-checking and rendering: a redacting LogValue() wins over the raw type.
type redactingValue struct{}

func (redactingValue) LogValue() slog.Value { return slog.StringValue("[redacted]") }

func TestLogHandlerLogValuer(t *testing.T) {
	var buf bytes.Buffer
	h := newLogHandler(&buf, slog.LevelDebug)
	log := slog.New(h)
	log.Info("leaking", slog.Any("token", redactingValue{}))
	out := buf.String()
	if !strings.Contains(out, "token=[redacted]") {
		t.Fatalf("record = %q, want the resolved LogValue form token=[redacted]", out)
	}
	if strings.Contains(out, "redactingValue") {
		t.Errorf("record = %q, the raw type leaked past the LogValue redaction", out)
	}
}

// TestLogHandlerGroupContract pins the baked-in group keys: attrs added via
// WithAttrs keep their keys, WithGroup prefixes only the record's attrs, and
// WithGroup("") is a no-op.
func TestLogHandlerGroupContract(t *testing.T) {
	var buf bytes.Buffer
	h := newLogHandler(&buf, slog.LevelDebug)
	log := slog.New(h)
	log.With("a", 1).WithGroup("g").Info("m", "b", 2)
	got := buf.String()
	if !strings.Contains(got, "m | a=1 g.b=2") {
		t.Fatalf("record = %q, want the ordering and keys 'm | a=1 g.b=2'", got)
	}

	buf.Reset()
	log.With("a", 1).WithGroup("g").WithGroup("").Info("m", "b", 2)
	got = buf.String()
	if !strings.HasSuffix(got, "m | a=1 g.b=2\n") {
		t.Fatalf("record = %q, WithGroup(\"\") must be a no-op ending 'm | a=1 g.b=2'", got)
	}
}

// TestLogHandlerMessageNewlines pins that a message containing \n and \r
// renders on exactly one physical line with the escapes visible.
func TestLogHandlerMessageNewlines(t *testing.T) {
	var buf bytes.Buffer
	h := newLogHandler(&buf, slog.LevelDebug)
	log := slog.New(h)
	log.Info("line1\nline2\rcr", slog.String("k", "v"))
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("record = %q, want exactly one physical line (single trailing newline)", out)
	}
	if !strings.Contains(out, "line1\\nline2\\rcr | k=v") {
		t.Fatalf("record = %q, want the escaped message 'line1\\nline2\\rcr | k=v'", out)
	}
}

// TestLogHandlerCRValue pins that a CR-only-bearings value is quoted (and
// strconv.Quote escapes the CR), like any whitespace-bearing value.
func TestLogHandlerCRValue(t *testing.T) {
	var buf bytes.Buffer
	h := newLogHandler(&buf, slog.LevelDebug)
	log := slog.New(h)
	log.Info("m", slog.String("cr", "abc\rdef"))
	got := buf.String()
	if !strings.Contains(got, `cr="abc\rdef"`) {
		t.Fatalf("record = %q, want the CR-bearing value quoted and escaped: cr=\"abc\\rdef\"", got)
	}
	if !strings.HasSuffix(got, "\n") || strings.Count(got, "\n") != 1 {
		t.Fatalf("record = %q, must stay on one physical line", got)
	}
}

// TestLogHandlerConcurrentHandle exercises many goroutines calling Handle on
// the same handler value to prove it is safe for concurrent use (the sink is
// mutex-guarded; the handler itself must be — go test -race would catch it).
func TestLogHandlerConcurrentHandle(t *testing.T) {
	sink := &lockedWriter{}
	h := newLogHandler(sink, slog.LevelDebug)
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "concurrent", 0)
	rec.Add(slog.String("k", "v"))

	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Handle(context.Background(), rec); err != nil {
				t.Errorf("Handle: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := strings.Count(sink.String(), "concurrent"); got != n {
		t.Fatalf("record count = %d, want %d", got, n)
	}
}

// lockedWriter is a mutex-guarded strings sink for the concurrency test.
type lockedWriter struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

var _ io.Writer = (*lockedWriter)(nil)
