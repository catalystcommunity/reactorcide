package worker

import (
	"context"
	"errors"
	"strings"

	"github.com/docker/docker/errdefs"
)

// Image pull failure classes (docs/workers.md "Image sets and pre-seed").
//
//   - Permanent: a retry with the same reference cannot help. The manifest
//     or a blob is not found, the content does not agree with the digest,
//     the registry refuses the credentials (unauthorized or denied), or the
//     reference is not valid. The operator or the alias owner must fix it.
//   - Transient: anything else (network, timeout, a 5xx answer, no disk
//     space). The worker tries again with backoff.

// PullError is an image pull failure with its class.
type PullError struct {
	Permanent bool
	Err       error
}

func (e *PullError) Error() string { return e.Err.Error() }
func (e *PullError) Unwrap() error { return e.Err }

// PermanentPullError marks err as a permanent pull failure.
func PermanentPullError(err error) error {
	if err == nil {
		return nil
	}
	return &PullError{Permanent: true, Err: err}
}

// TransientPullError marks err as a transient pull failure.
func TransientPullError(err error) error {
	if err == nil {
		return nil
	}
	return &PullError{Err: err}
}

// permanentPullText are lower case texts of registry and runtime errors that
// a retry cannot fix. The OCI distribution error codes are MANIFEST_UNKNOWN,
// BLOB_UNKNOWN, NAME_UNKNOWN, UNAUTHORIZED, DENIED, and NAME_INVALID.
var permanentPullText = []string{
	"manifest unknown",
	"manifest_unknown",
	"blob unknown",
	"blob_unknown",
	"name unknown",
	"name_unknown",
	"name invalid",
	"name_invalid",
	"not found",
	"notfound",
	"unauthorized",
	"authentication required",
	"denied",
	"forbidden",
	"invalid reference",
	"invalid image name",
	"invalidimagename",
	"is not a valid",
	"digest mismatch",
	"digest does not match",
	"does not match the digest",
	"unexpected digest",
	"content digest",
	"mismatch",
	"invalid blob digest",
	"invalid image digest",
	"invalid layer digest",
	"invalid digest",
	"has no digest",
	"nosuchkey",
	"nosuchbucket",
}

// transientPullText wins over permanentPullText: a disk that is full, a
// timeout, or a 5xx answer can contain a word of the permanent list. The
// lists have no bare status numbers: an error text often has a reference
// with a hex digest, which can contain any number.
var transientPullText = []string{
	"no space left",
	"disk space",
	"timeout",
	"timed out",
	"deadline exceeded",
	"connection refused",
	"connection reset",
	"temporary failure",
	"too many requests",
	"internal server error",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
	"no such host",
	"dial tcp",
	"i/o timeout",
	"executable file not found",
}

// IsPermanentPullError reports whether a pull failure is permanent. A
// PullError gives its class. A Docker error class (not found, unauthorized,
// forbidden, invalid parameter) is permanent. Else the text of the error
// decides; an error with no known text is transient.
func IsPermanentPullError(err error) bool {
	if err == nil {
		return false
	}
	var pe *PullError
	if errors.As(err, &pe) {
		return pe.Permanent
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errdefs.IsNotFound(err) || errdefs.IsUnauthorized(err) || errdefs.IsForbidden(err) || errdefs.IsInvalidParameter(err) {
		return true
	}
	return permanentPullTextIn(err.Error())
}

// permanentPullTextIn classifies an error text.
func permanentPullTextIn(text string) bool {
	text = strings.ToLower(text)
	for _, t := range transientPullText {
		if strings.Contains(text, t) {
			return false
		}
	}
	for _, t := range permanentPullText {
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}
