package api

import (
	"errors"
	"fmt"
)

// Sentinel errors mapped to control-API error codes.
var (
	ErrNotFound    = errors.New("not found")
	ErrBadRequest  = errors.New("bad request")
	ErrConflict    = errors.New("conflict")
	ErrUnsupported = errors.New("unsupported")
)

// NotFound formats a not-found error.
func NotFound(what string, id any) error {
	return fmt.Errorf("%w: %s %v", ErrNotFound, what, id)
}

// BadRequest formats a bad-request error.
func BadRequest(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRequest, fmt.Sprintf(format, a...))
}

// CodeOf maps an error to a code and HTTP status.
func CodeOf(err error) (code string, status int) {
	var ae *Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case CodeNotFound:
			return ae.Code, 404
		case CodeBadRequest:
			return ae.Code, 400
		case CodeConflict:
			return ae.Code, 409
		case CodeUnsupported:
			return ae.Code, 501
		default:
			return ae.Code, 500
		}
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return CodeNotFound, 404
	case errors.Is(err, ErrBadRequest):
		return CodeBadRequest, 400
	case errors.Is(err, ErrConflict):
		return CodeConflict, 409
	case errors.Is(err, ErrUnsupported):
		return CodeUnsupported, 501
	}
	return CodeInternal, 500
}

// Preset describes a rule preset.
type Preset struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Params      map[string]any `json:"params"`
}
