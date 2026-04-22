package logger

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedNow() time.Time {
	return time.Date(2026, 4, 22, 10, 30, 45, int(123*time.Millisecond), time.UTC)
}

func newTW(buf io.WriteCloser) *TimestampedWriter {
	w := NewTimestampedWriter(buf)
	w.nowFn = fixedNow
	return w
}

func TestTimestampedWriter_PrefixesPlainLine(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	n, err := w.Write([]byte("hello world\n"))
	require.NoError(t, err)
	assert.Equal(t, 12, n)
	assert.Equal(t, "2026/04/22 10:30:45.123 hello world\n", buf.String())
}

func TestTimestampedWriter_MultipleLinesInOneWrite(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	_, err := w.Write([]byte("line1\nline2\nline3\n"))
	require.NoError(t, err)

	expected := "2026/04/22 10:30:45.123 line1\n" +
		"2026/04/22 10:30:45.123 line2\n" +
		"2026/04/22 10:30:45.123 line3\n"
	assert.Equal(t, expected, buf.String())
}

func TestTimestampedWriter_SplitLineAcrossWrites(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	_, err := w.Write([]byte("partial "))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "no output yet, still waiting for newline")

	_, err = w.Write([]byte("line\n"))
	require.NoError(t, err)
	assert.Equal(t, "2026/04/22 10:30:45.123 partial line\n", buf.String(),
		"timestamp should appear only once for a line split across writes")
}

func TestTimestampedWriter_PartialLineFlushedOnClose(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	_, err := w.Write([]byte("no newline here"))
	require.NoError(t, err)
	assert.Empty(t, buf.String())

	require.NoError(t, w.Close())
	assert.Equal(t, "2026/04/22 10:30:45.123 no newline here", buf.String())
}

func TestTimestampedWriter_EmptyWrite(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	n, err := w.Write(nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Empty(t, buf.String())
}

// TestTimestampedWriter_AlwaysPrependsEvenIfAlreadyTimestamped documents the
// deliberate choice: the writer does not try to detect pre-existing
// timestamps. Callers opt in knowing their stream needs a prefix; double
// timestamps on streams that already carry one are expected and preferred
// over the silent false-negatives a detector would produce for logfmt,
// syslog-style dates, RFC3339Nano, JSON with embedded "timestamp" fields,
// etc.
func TestTimestampedWriter_AlwaysPrependsEvenIfAlreadyTimestamped(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	line := "2025/03/14 15:37:20 [emerg] 7#7: host not found\n"
	_, err := w.Write([]byte(line))
	require.NoError(t, err)
	assert.Equal(t, "2026/04/22 10:30:45.123 "+line, buf.String())
}

func TestTimestampedWriter_OversizedLineIsFlushed(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	big := bytes.Repeat([]byte("a"), maxLineBufferBytes+16)
	_, err := w.Write(big)
	require.NoError(t, err)
	assert.NotEmpty(t, buf.String(), "oversized line should be flushed even without newline")
	assert.True(t, strings.HasPrefix(buf.String(), "2026/04/22 10:30:45.123 "),
		"oversized flush should still get a timestamp prefix")
}

func TestTimestampedWriter_Close(t *testing.T) {
	t.Run("close propagates to underlying writer", func(t *testing.T) {
		closed := false
		w := NewTimestampedWriter(&closeTracker{closed: &closed})
		require.NoError(t, w.Close())
		assert.True(t, closed)
	})

	t.Run("close propagates error", func(t *testing.T) {
		w := NewTimestampedWriter(&errWriteCloser{closeErr: errors.New("close failed")})
		err := w.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close failed")
	})

	t.Run("flush error on close does not block close", func(t *testing.T) {
		// a partial line remains in the buffer; the underlying Write fails
		// but Close must still run and return the close error (nil here).
		underlying := &errWriteCloser{writeErr: errors.New("write failed")}
		w := NewTimestampedWriter(underlying)
		_, err := w.Write([]byte("partial no newline"))
		require.NoError(t, err, "write with no newline should not surface the downstream write error")
		require.NoError(t, w.Close(), "close should not surface the flush error, just log it")
	})
}

func TestTimestampedWriter_WriteErrorPropagates(t *testing.T) {
	w := NewTimestampedWriter(&errWriteCloser{writeErr: errors.New("boom")})
	n, err := w.Write([]byte("hello\n"))
	require.Error(t, err)
	assert.Equal(t, 0, n)
}

func TestTimestampedWriter_ConcurrentWrites(t *testing.T) {
	buf := &syncBuffer{}
	w := newTW(buf)

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, _ = w.Write([]byte("msg line\n"))
		})
	}
	wg.Wait()

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Len(t, lines, 20)
	for _, line := range lines {
		assert.Equal(t, "2026/04/22 10:30:45.123 msg line", line,
			"each line must have exactly one timestamp prefix")
	}
}

type closeTracker struct {
	closed *bool
}

func (c *closeTracker) Write(p []byte) (int, error) { return len(p), nil }
func (c *closeTracker) Close() error                { *c.closed = true; return nil }

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Close() error { return nil }

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

var _ io.WriteCloser = (*closeTracker)(nil)
var _ io.WriteCloser = (*syncBuffer)(nil)
