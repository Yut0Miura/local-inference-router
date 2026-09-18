package router

import (
	"io"
	"mime"
	"net/http"
	"strings"
)

// copyBufferSize is the size of the reusable relay buffer. Response bodies are
// never accumulated in memory.
const copyBufferSize = 32 * 1024

// flushWriter pushes every write to the client immediately. Without it a
// server-sent event stream would sit in a buffer and arrive in bursts.
type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.f.Flush()
	return n, err
}

// isEventStream reports whether an upstream response is a server-sent event
// stream. Media type parameters such as charset are allowed.
func isEventStream(header http.Header) bool {
	contentType := header.Get("Content-Type")
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// Fall back to the leading token: a malformed parameter list must not
		// turn a stream into a buffered response.
		mediaType = strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	}
	return strings.EqualFold(mediaType, "text/event-stream")
}

// relay copies an upstream body downstream. Event streams are flushed after
// every write; other responses stream through the same buffer without being
// parsed or rewritten.
func relay(dst io.Writer, src io.Reader) error {
	buffer := make([]byte, copyBufferSize)
	_, err := io.CopyBuffer(dst, src, buffer)
	return err
}
