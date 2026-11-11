package backend

import (
	"io"
	"net/http"
)

const maxDiscardBodyBytes int64 = 32 * 1024

// DrainAndClose discards a bounded amount of an unused response body before
// closing it so small responses can leave their HTTP connection reusable.
func DrainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}

	_, _ = io.CopyN(
		io.Discard,
		resp.Body,
		maxDiscardBodyBytes+1,
	)

	_ = resp.Body.Close()
}
