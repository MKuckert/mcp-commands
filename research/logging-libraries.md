# Go Logging Library Research

Requirements:

1. Simple interface to log different severities (at least debug, info, warning)
2. Wrap `stdout` and `stderr`
3. Purely configurable in code (no config files / env-var magic)
4. Optional: colors in a TTY

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
- **stdout/stderr:** handlers take any `io.Writer`; routing debug→stdout and info+→stderr is two handlers plus `slog.NewRouteHandler`/`slog.NewMultiHandler`.
- **Code-only config:** yes — `slog.HandlerOptions` (level, time format, attribute replacement) + `NewJSONHandler` for machine output.
- **Colors:** none built in. Drop-in colored text handler: [`lmittmann/tint`](https://github.com/lmittmann/tint) (auto-detects TTY).
- Zero dependencies, idiomatic modern Go, no maintenance risk.

### Severity split by stream (slog)

```go
errH := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
dbgH := tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelDebug, TimeFormat: time.Kitchen})

log := slog.New(slog.NewRouteHandler(errH, func(r slog.Record) slog.Handler {
    if r.Level < slog.LevelInfo {
        return dbgH
    }
    return nil // keep default (errH)
}))
```

For a simple case one handler on stderr plus a level setting in code is enough.

## 2. `rs/zerolog`

```go
w := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339} // NoColor: true to disable
log := zerolog.New(w).Level(zerolog.DebugLevel).With().Timestamp().Logger()
log.Info().Str("k", "v").Msg("hello")
```

- Zero-allocation JSON; `ConsoleWriter` gives colored, TTY-aware pretty output with fully customizable `FormatLevel`/`FormatMessage`/`PartsOrder`.
- Writer is an `io.Writer` (`zerolog.Output(...)`); level set in code via `.Level(...)`.
- Builder-style API (`log.Warn().Msg(...)`) is fluent but noisier than slog.
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

## 4. `sirupsen/logrus`

- Classic: `logrus.SetOutput(os.Stderr)`, `SetLevel(...)`, `TextFormatter{FullTimestamp: true}` — auto-colors when a TTY.
- Simple and well-known, but unstructured, slower, and in maintenance mode. No reason to start new.

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

## Recommendation

- **Default: `log/slog`** (+ `lmittmann/tint` for TTY colors). Meets all four requirements with zero to one dependency, idiomatic modern Go.
- **If performance is critical: `zerolog`** — same shape, zero allocs, built-in color console writer.
- Skip zap/logrus unless the project already standardizes on one.
