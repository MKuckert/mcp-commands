# Go Logging Library Research

Requirements:

1. Simple interface to log different severities (at least debug, info, warning)
2. Wrap `stdout` and `stderr`
3. Purely configurable in code (no config files / env-var magic)
4. Optional: colors in a TTY
5. Easily testable (capture/assert log output in unit tests)

---

## 1. `log/slog` — standard library (Go ≥ 1.21) ⭐ top pick

```go
import "log/slog"

log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
slog.SetDefault(log)

log.Debug("d")
log.Info("i", "key", "value") // structured fields
log.Warn("w")
log.Error("e")
```

- **Severities:** `Debug`/`Info`/`Warn`/`Error`, custom levels via `slog.Level`.
- **stdout/stderr:** handlers take any `io.Writer`; routing debug→stdout and info+→stderr is two handlers plus a small custom routing handler (`NewMultiHandler` fans out to *all* handlers, so it cannot split by level).
- **Code-only config:** yes — `slog.HandlerOptions` (level, time format, attribute replacement) + `NewJSONHandler` for machine output.
- **Colors:** none built in. Drop-in colored text handler: [`lmittmann/tint`](https://github.com/lmittmann/tint) (auto-detects TTY).
- Zero dependencies, idiomatic modern Go, no maintenance risk.

**Testable:** handlers take any `io.Writer`, so tests construct the handler on a `bytes.Buffer` and assert against it; per-test loggers via `slog.New(...)` avoid `SetDefault` global state entirely.

### Severity split by stream (slog)

```go
errH := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
// Debug lines go to stdout (kept out of stderr in stdio-server mode).
// tint.NewHandler(os.Stdout, &tint.Options{Level: slog.LevelDebug, TimeFormat: time.Kitchen}) for colored debug output.
dbgH := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})

// slog has no built-in level router: a tiny custom Handler splits by level
// (Go 1.25+ handler signatures; Go 1.21–1.24 omit the ctx parameters).
type route struct{ def, low slog.Handler }

func (r route) Enabled(ctx context.Context, l slog.Level) bool {
    if l < slog.LevelInfo {
        return r.low.Enabled(ctx, l)
    }
    return r.def.Enabled(ctx, l)
}

func (r route) Handle(ctx context.Context, rec slog.Record) error {
    if rec.Level < slog.LevelInfo {
        return r.low.Handle(ctx, rec)
    }
    return r.def.Handle(ctx, rec)
}

func (r route) WithAttrs(a []slog.Attr) slog.Handler {
    return route{r.def.WithAttrs(a), r.low.WithAttrs(a)}
}

func (r route) WithGroup(g string) slog.Handler {
    return route{r.def.WithGroup(g), r.low.WithGroup(g)}
}

log := slog.New(route{def: errH, low: dbgH})
```

For a simple case one handler on stderr plus a level setting in code is enough.

## 2. `rs/zerolog`

```go
w := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339} // NoColor: true to disable
log := zerolog.New(w).Level(zerolog.DebugLevel).With().Timestamp().Logger()
log.Info().Str("k", "v").Msg("hello")
```

- Zero-allocation JSON; `ConsoleWriter` gives colored pretty output with fully customizable `FormatLevel`/`FormatMessage`/`PartsOrder` — but no TTY auto-detection: set `NoColor` yourself when piped.
- Writer is an `io.Writer` (`zerolog.Output(...)`); level set in code via `.Level(...)`.
- Builder-style API (`log.Warn().Msg(...)`) is fluent but noisier than slog.
- **Testable:** same `io.Writer` injection — `zerolog.New(bytes.Buffer{})`.
- Best when allocation/benchmark numbers matter; otherwise slog is simpler.

## 3. `uber-go/zap`

```go
log, _ := zap.NewDevelopment() // colored console → stderr
log, _ := zap.NewProduction()  // JSON → stderr
log = log.WithOptions(zap.AddCaller())
log.Debug("d"); log.Info("i"); log.Warn("w"); log.Error("e")
```

- `NewDevelopment` = colored console, `NewProduction` = JSON; both from `zap.Config` (pure code).
- Output writer settable via `zapcore.NewCore(enc, zapcore.AddSync(os.Stdout), level)`.
- Fastest in benchmarks (0 allocs), `LevelEnabler` for levels.
- Heavier dependency; non-structured calls need the `Sugar` interface. Overkill for a small CLI.
- **Testable:** purpose-built `zaptest/observer` in-memory core — the nicest assert API of the group (`observedLogs.LogEntry()` with level/args matchers).

## 4. `sirupsen/logrus`

- Classic: `logrus.SetOutput(os.Stderr)`, `SetLevel(...)`, `TextFormatter{FullTimestamp: true}` — auto-colors when a TTY.
- Simple and well-known, but unstructured, slower, and in maintenance mode. No reason to start new.
- **Testable:** `SetOutput(&buf)` + a `Hook` for entry-level assertions; works, but the global-default logger makes per-test isolation clumsier.

## Others considered

| Library | Verdict |
|---|---|
| `gookit/log` | Lightweight, multi-handler, built-in console color formatter; decent single-dep option |
| `apex/log` | Minimal leveled logger, no colors; too bare for the color requirement |
| `go-logr` | Adapter over zap/logrus/etc.; added indirection, not worth it at this scale |
| `golang.org/x/exp` handlers | Superseded by stdlib `log/slog` |

## Performance snapshot

From community benchmarks (relative, single-line structured log):

| Library | ~ns/op | allocs/op |
|---|---|---|
| zap | ~192 | 0 |
| zerolog | ~147 | 0 |
| slog | ~842 | 3 |
| logrus | ~4700+ | many |

slog is slower than the zero-alloc pair, but at CLI/tool logging volumes the difference is noise.

## Testability — cross-cutting

All four support the same essential pattern: construct the logger on a `bytes.Buffer` in tests, assert on captured text/entries. That maps directly onto this repo's `liveEnv` injection pattern (`server.go:42-50`): a logger built on `env.stderr` in prod and on a buffer in tests needs no global state and keeps `t.Parallel()` safe. The in-memory *entry* APIs (zaptest/observer, slog record capture via a custom handler) are nicer than string matching, but string-matching captured output is sufficient for asserting the `Warning:`/`Error:` shapes this repo currently emits.

## Recommendation

- **Default: `log/slog`** (+ `lmittmann/tint` for TTY colors). Meets all five requirements with zero to one dependency, idiomatic modern Go.
- **If performance is critical: `zerolog`** — same shape, zero allocs, built-in color console writer (no TTY auto-detect).
- Skip zap/logrus unless the project already standardizes on one.
