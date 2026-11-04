package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// HTTPStatusError is a non-200 reply from a provider.
type HTTPStatusError struct {
	Provider string
	Code     int
	Body     string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("provider %s: status %d: %s", e.Provider, e.Code, e.Body)
}

// RetryBackoff is the wait before each successive retry of a transient failure
// (so len+1 attempts in total). A var so tests can shrink it.
var RetryBackoff = []time.Duration{2 * time.Second, 6 * time.Second}

// IsTransient reports whether err looks like an outage or hiccup worth retrying
// (network failure, 408/425/429/5xx, a reset or idle-timed-out stream) as
// opposed to a request the provider will keep rejecting (400/401/403/404,
// context too long). Cancellation is never transient.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var se *HTTPStatusError
	if errors.As(err, &se) {
		switch se.Code {
		case 408, 425, 429:
			return true
		}
		return se.Code >= 500
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{
		"stream error", "internal_error", "goaway", "connection reset", "connection refused",
		"broken pipe", "unexpected eof", "idle timeout", "no such host", "tls:", "http2:",
		"server_error", "overloaded", "temporarily unavailable", "rate limit", "(code 5", "(code 429",
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// SleepBackoff waits d or until ctx ends; it reports whether the full wait elapsed.
func SleepBackoff(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
