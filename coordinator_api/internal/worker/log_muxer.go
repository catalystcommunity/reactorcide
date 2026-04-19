package worker

import (
	"bytes"
	"io"
	"sync"
)

// BuilderLogPrefix is prepended to every line of sidecar log output so
// operators can tell at a glance which container produced which line. The
// callers that apply secret masking read line-by-line, so a deterministic
// line-level prefix preserves masker correctness.
const BuilderLogPrefix = "[builder] "

// newTaggingWriter returns an io.Writer that buffers its input until complete
// lines arrive, then writes each line to dest under mu after the given prefix.
// Callers creating multiple taggers that share a dest should share a single
// mu so lines are atomic w.r.t. each other even when streams interleave.
//
// The mutex only protects ordering of dest.Write calls; each tagger keeps its
// own buffer so a partial line from stream A won't be corrupted by stream B.
func newTaggingWriter(dest io.Writer, mu *sync.Mutex, prefix string) io.Writer {
	return &taggingWriter{
		dest:   dest,
		mu:     mu,
		prefix: []byte(prefix),
	}
}

type taggingWriter struct {
	dest   io.Writer
	mu     *sync.Mutex
	prefix []byte
	buf    []byte
}

func (t *taggingWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	for {
		idx := bytes.IndexByte(t.buf, '\n')
		if idx < 0 {
			break
		}
		line := t.buf[:idx+1]
		t.mu.Lock()
		if len(t.prefix) > 0 {
			if _, err := t.dest.Write(t.prefix); err != nil {
				t.mu.Unlock()
				return 0, err
			}
		}
		if _, err := t.dest.Write(line); err != nil {
			t.mu.Unlock()
			return 0, err
		}
		t.mu.Unlock()
		t.buf = t.buf[idx+1:]
	}
	return len(p), nil
}

// Flush writes any trailing non-newline-terminated bytes, terminated with a
// newline, so the caller's line-scanner sees a complete final record. Call
// this when the upstream stream closes.
func (t *taggingWriter) Flush() error {
	if len(t.buf) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.prefix) > 0 {
		if _, err := t.dest.Write(t.prefix); err != nil {
			return err
		}
	}
	if _, err := t.dest.Write(t.buf); err != nil {
		return err
	}
	_, err := t.dest.Write([]byte("\n"))
	t.buf = nil
	return err
}
