package logger

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"time"

	log "github.com/go-pkgz/lgr"
)

// timestampFormat is the hardcoded layout used for the leading timestamp
// prepended to every .err log line. It mirrors the nginx error.log layout
// with millisecond precision appended.
const timestampFormat = "2006/01/02 15:04:05.000"

// maxLineBufferBytes caps how much we buffer for a single un-terminated line
// before flushing it preemptively. This protects us from pathological
// containers that never emit newlines. 64 KiB is an order of magnitude larger
// than any realistic log line.
const maxLineBufferBytes = 64 * 1024

// TimestampedWriter wraps an io.WriteCloser and unconditionally prepends a
// timestamp to every line. It is safe for concurrent use.
//
// Behavior:
//   - Input bytes are buffered one line at a time (ended by '\n').
//   - Every line is prefixed with the current time formatted with
//     timestampFormat, followed by a single space.
//   - Any remainder without a trailing newline is flushed on Close and also
//     whenever the internal buffer grows past maxLineBufferBytes.
//
// The caller is expected to opt in to wrapping only when the upstream stream
// is known to be free-form text without its own timestamps (typical for
// docker container stderr). No attempt is made to detect pre-existing
// timestamps; deterministic always-prepend is simpler and avoids the
// false-positive/false-negative footguns of format sniffing.
type TimestampedWriter struct {
	w     io.WriteCloser
	nowFn func() time.Time

	mu      sync.Mutex
	lineBuf bytes.Buffer
}

// NewTimestampedWriter returns a TimestampedWriter that prepends a fixed-format
// timestamp to every line written through it.
func NewTimestampedWriter(w io.WriteCloser) *TimestampedWriter {
	return &TimestampedWriter{
		w:     w,
		nowFn: time.Now,
	}
}

// Write accumulates bytes into an internal line buffer and flushes complete
// lines to the underlying writer.
func (t *TimestampedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	start := 0
	for i, b := range p {
		if b == '\n' {
			t.lineBuf.Write(p[start : i+1])
			if err := t.flushLocked(); err != nil {
				return 0, err
			}
			start = i + 1
		}
	}

	if start < len(p) {
		t.lineBuf.Write(p[start:])
		if t.lineBuf.Len() >= maxLineBufferBytes {
			if err := t.flushLocked(); err != nil {
				return 0, err
			}
		}
	}

	return len(p), nil
}

// Close flushes any buffered partial line and closes the underlying writer.
// If the final flush fails (e.g. a panic trace that never terminated with a
// newline) the error is logged so it is not lost; Close still returns the
// close error from the underlying writer.
func (t *TimestampedWriter) Close() error {
	t.mu.Lock()
	if t.lineBuf.Len() > 0 {
		if err := t.flushLocked(); err != nil {
			log.Printf("[WARN] timestamped writer: failed to flush final line on close: %v", err)
		}
	}
	t.mu.Unlock()
	return t.w.Close()
}

// flushLocked writes the contents of lineBuf to the underlying writer,
// prefixed with the current timestamp. The buffer is reset regardless of
// whether the write succeeded. Caller must hold t.mu.
func (t *TimestampedWriter) flushLocked() error {
	stamp := t.nowFn().Format(timestampFormat)
	payload := fmt.Appendf(nil, "%s %s", stamp, t.lineBuf.Bytes())
	_, err := t.w.Write(payload)
	t.lineBuf.Reset()
	return err
}
