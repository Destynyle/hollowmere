// Package logs configures structured JSON logging for the server.
//
// Log records are written by a background goroutine so that a slow or
// blocked output never stalls the game loop: the writer only hands bytes to
// a buffered channel, and drops records when that channel is full.
package logs

import (
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

// AsyncWriter turns any writer into a non-blocking one.
type AsyncWriter struct {
	lines   chan []byte
	out     io.Writer
	done    chan struct{}
	once    sync.Once
	dropped atomic.Int64
}

// NewAsyncWriter starts the background writer. Records are duplicated to
// every writer in outs.
func NewAsyncWriter(outs ...io.Writer) *AsyncWriter {
	var out io.Writer
	switch len(outs) {
	case 0:
		out = os.Stdout
	case 1:
		out = outs[0]
	default:
		out = io.MultiWriter(outs...)
	}
	w := &AsyncWriter{
		lines: make(chan []byte, 8192),
		out:   out,
		done:  make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *AsyncWriter) run() {
	defer close(w.done)
	for line := range w.lines {
		_, _ = w.out.Write(line)
	}
}

// Write queues one record; it never blocks.
func (w *AsyncWriter) Write(p []byte) (int, error) {
	buf := make([]byte, len(p))
	copy(buf, p)
	select {
	case w.lines <- buf:
	default:
		w.dropped.Add(1)
	}
	return len(p), nil
}

// Dropped reports how many records were discarded because the queue was
// full (exported as a metric).
func (w *AsyncWriter) Dropped() int64 { return w.dropped.Load() }

// Close flushes pending records.
func (w *AsyncWriter) Close() {
	w.once.Do(func() {
		close(w.lines)
		<-w.done
	})
}

// New builds a JSON logger on top of an AsyncWriter.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// Discard returns a logger that throws everything away (tests).
func Discard() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// ParseLevel maps a flag value to a slog level.
func ParseLevel(s string) slog.Level {
	switch s {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "warn", "WARN":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
