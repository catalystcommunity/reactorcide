package vcs

import "errors"

var (
	// ErrUnsupportedProvider indicates an unsupported VCS provider
	ErrUnsupportedProvider = errors.New("unsupported VCS provider")

	// ErrMissingEventHeader indicates the webhook event header is missing
	ErrMissingEventHeader = errors.New("missing event header")

	// ErrMissingSignature indicates the webhook signature is missing
	ErrMissingSignature = errors.New("missing webhook signature")

	// ErrInvalidSignature indicates the webhook signature is invalid
	ErrInvalidSignature = errors.New("invalid webhook signature")

	// ErrInvalidPayload indicates the webhook payload is invalid
	ErrInvalidPayload = errors.New("invalid webhook payload")
)