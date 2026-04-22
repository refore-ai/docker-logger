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

func TestTimestampedWriter_DetectsExistingTimestamps(t *testing.T) {
	cases := map[string]string{
		"nginx error":                 "2025/03/14 15:37:20 [emerg] 7#7: host not found\n",
		"ISO space":                   "2025-03-14 15:37:20 some application log\n",
		"ISO 8601 with T":             "2025-03-14T15:37:20 app log\n",
		"RFC3339 with Z":              "2025-03-14T15:37:20.123Z java spring\n",
		"RFC3339 with offset":         "2025-03-14T15:37:20+08:00 event\n",
		"bracketed ISO":               "[2025-03-14T15:37:20,123] elasticsearch INFO foo\n",
		"bracketed date":              "[2025-03-14 15:37:20] log4j WARN bar\n",
		"Apache common":               "14/Mar/2025:15:37:20 +0000 GET /\n",
		"bracketed Apache":            "[14/Mar/2025:15:37:20 +0000] GET /\n",
		"leading whitespace + nginx":  "  2025/03/14 15:37:20 indented\n",
	}

	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			buf := &wrMock{}
			w := newTW(buf)
			_, err := w.Write([]byte(line))
			require.NoError(t, err)
			assert.Equal(t, line, buf.String(), "line should be passed through unchanged")
		})
	}
}

func TestTimestampedWriter_MixedDetectedAndPlainLines(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	input := "2025/03/14 15:37:20 nginx already has ts\n" +
		"plain error without timestamp\n" +
		"2025-03-14T15:37:20Z another ts\n"
	_, err := w.Write([]byte(input))
	require.NoError(t, err)

	expected := "2025/03/14 15:37:20 nginx already has ts\n" +
		"2026/04/22 10:30:45.123 plain error without timestamp\n" +
		"2025-03-14T15:37:20Z another ts\n"
	assert.Equal(t, expected, buf.String())
}

func TestTimestampedWriter_DoesNotDetectBogusPatterns(t *testing.T) {
	buf := &wrMock{}
	w := newTW(buf)

	// looks a bit like a date but is not a timestamp prefix
	_, err := w.Write([]byte("version 1.2.3 2025\n"))
	require.NoError(t, err)
	assert.Equal(t, "2026/04/22 10:30:45.123 version 1.2.3 2025\n", buf.String())
}

func TestTimestampedWriter_CustomFormat(t *testing.T) {
	buf := &wrMock{}
	w := NewTimestampedWriterWithFormat(buf, "2006-01-02 15:04:05.000")
	w.nowFn = fixedNow

	_, err := w.Write([]byte("msg\n"))
	require.NoError(t, err)
	assert.Equal(t, "2026-04-22 10:30:45.123 msg\n", buf.String())
}

func TestTimestampedWriter_EmptyFormatFallsBackToDefault(t *testing.T) {
	buf := &wrMock{}
	w := NewTimestampedWriterWithFormat(buf, "")
	assert.Equal(t, DefaultTimestampFormat, w.format)
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
