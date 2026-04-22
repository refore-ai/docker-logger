package logger

import (
	"bytes"
	"io"
	"regexp"
	"sync"
	"time"
)

// DefaultTimestampFormat is the default timestamp format used by TimestampedWriter.
// It mirrors the nginx error.log layout with millisecond precision appended:
// "2006/01/02 15:04:05.000".
const DefaultTimestampFormat = "2006/01/02 15:04:05.000"

// DefaultTimestampPattern matches the most common timestamp prefixes found at
// the beginning of application log lines. When a line already matches this
// pattern, TimestampedWriter will leave the line untouched instead of
// prepending another timestamp.
//
// Supported prefixes (with optional leading whitespace and an optional
// leading "["):
//
//   - nginx / Go log           : 2025/03/14 15:37:20
//   - ISO 8601 / RFC3339       : 2025-03-14T15:37:20[.fff][Z|±hh:mm]
//   - common SQL / Python      : 2025-03-14 15:37:20[.fff|,fff]
//   - Elasticsearch / Log4j    : [2025-03-14T15:37:20,123]
//   - Apache / CLF             : 14/Mar/2025:15:37:20 +0000
var DefaultTimestampPattern = regexp.MustCompile(
	`^\s*\[?(` +
		// 2025/03/14 15:37:20 or 2025-03-14 15:37:20 or 2025-03-14T15:37:20
		`\d{4}[-/]\d{2}[-/]\d{2}[ T]\d{2}:\d{2}:\d{2}` +
		`|` +
		// 14/Mar/2025:15:37:20 (Apache)
		`\d{2}/[A-Za-z]{3}/\d{4}:\d{2}:\d{2}:\d{2}` +
		`)`,
)

// maxLineBufferBytes caps how much we buffer for a single un-terminated line
// before flushing it preemptively. This protects us from pathological
// containers that never emit newlines. 64 KiB is an order of magnitude larger
// than any realistic log line.
const maxLineBufferBytes = 64 * 1024

// TimestampedWriter wraps an io.WriteCloser and ensures every line has a
// leading timestamp. It is safe for concurrent use.
//
// Behavior:
//   - Input bytes are buffered one line at a time (ended by '\n').
//   - On flush, if the line already starts with a recognized timestamp (see
//     DefaultTimestampPattern) it is written as-is.
//   - Otherwise the line is prefixed with the current time formatted with
//     Format, followed by a single space.
//   - Any remainder without a trailing newline is flushed on Close and also
//     whenever the internal buffer grows past maxLineBufferBytes.
type TimestampedWriter struct {
	w        io.WriteCloser
	format   string
	detector *regexp.Regexp
	nowFn    func() time.Time

	mu      sync.Mutex
	lineBuf bytes.Buffer
}

// NewTimestampedWriter returns a TimestampedWriter using DefaultTimestampFormat
// and DefaultTimestampPattern.
func NewTimestampedWriter(w io.WriteCloser) *TimestampedWriter {
	return NewTimestampedWriterWithFormat(w, DefaultTimestampFormat)
}

// NewTimestampedWriterWithFormat returns a TimestampedWriter with a custom
// time format (see time.Layout). An empty format falls back to
// DefaultTimestampFormat.
func NewTimestampedWriterWithFormat(w io.WriteCloser, format string) *TimestampedWriter {
	if format == "" {
		format = DefaultTimestampFormat
	}
	return &TimestampedWriter{
		w:        w,
		format:   format,
		detector: DefaultTimestampPattern,
		nowFn:    time.Now,
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
			if err := t.flushLocked(true); err != nil {
				return 0, err
			}
			start = i + 1
		}
	}

	if start < len(p) {
		t.lineBuf.Write(p[start:])
		if t.lineBuf.Len() >= maxLineBufferBytes {
			if err := t.flushLocked(false); err != nil {
				return 0, err
			}
		}
	}

	return len(p), nil
}

// Close flushes any buffered partial line and closes the underlying writer.
// The close error from the underlying writer is returned even if the flush
// itself fails; the flush error is discarded because by definition the stream
// is shutting down.
func (t *TimestampedWriter) Close() error {
	t.mu.Lock()
	if t.lineBuf.Len() > 0 {
		_ = t.flushLocked(false)
	}
	t.mu.Unlock()
	return t.w.Close()
}

// flushLocked writes the contents of lineBuf to the underlying writer,
// optionally prefixing it with a timestamp. Caller must hold t.mu.
// The complete argument indicates whether the buffered line ended with '\n';
// a partial line still gets a prefix so that its timestamp reflects the moment
// it was observed by this writer.
func (t *TimestampedWriter) flushLocked(complete bool) error {
	line := t.lineBuf.Bytes()
	var payload []byte

	if t.detector != nil && t.detector.Match(line) {
		payload = line
	} else {
		stamp := t.nowFn().Format(t.format)
		payload = make([]byte, 0, len(stamp)+1+len(line))
		payload = append(payload, stamp...)
		payload = append(payload, ' ')
		payload = append(payload, line...)
	}

	_, err := t.w.Write(payload)
	t.lineBuf.Reset()
	_ = complete // kept for clarity/future use
	return err
}
