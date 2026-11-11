package domain

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Code       string
	Message    string
	StatusCode int
	Retryable  bool
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func WrapError(status int, code, message string, retryable bool, err error) *Error {
	return &Error{
		Code:       code,
		Message:    message,
		StatusCode: status,
		Retryable:  retryable,
		Err:        err,
	}
}

func ErrorOrDefault(err error) *Error {
	if err == nil {
		return nil
	}
	var target *Error
	if errors.As(err, &target) {
		return target
	}
	return &Error{
		Code:       "INTERNAL_ERROR",
		Message:    "internal server error",
		StatusCode: http.StatusInternalServerError,
		Retryable:  false,
		Err:        err,
	}
}
