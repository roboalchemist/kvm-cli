package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrorDetail is the inner error object of a StructuredError envelope.
//
// Recoverable tells an agent whether retrying the same command after a
// correction (fix the arguments, re-authenticate, wait for the network) can
// plausibly succeed. Suggestion is an optional, actionable next step. Both are
// additive: consumers that only read code/message are unaffected.
type ErrorDetail struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Recoverable bool   `json:"recoverable"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// StructuredError is the JSON error envelope emitted in JSON mode:
//
//	{"error": {"code": "...", "message": "..."}}
type StructuredError struct {
	Error ErrorDetail `json:"error"`
}

// CodedError attaches a stable, machine-readable code to an error. Commands can
// return one of these to control the code reported by --json error output.
type CodedError struct {
	Code    string
	Message string
	Err     error
}

// Error implements the error interface.
func (e *CodedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

// Unwrap exposes the wrapped error for errors.Is / errors.As.
func (e *CodedError) Unwrap() error { return e.Err }

// NewCodedError creates an error carrying an explicit code and message.
func NewCodedError(code, message string) error {
	return &CodedError{Code: code, Message: message}
}

// WrapCodedError wraps err with an explicit code. When message is empty the
// wrapped error's message is used.
func WrapCodedError(code string, err error, message string) error {
	return &CodedError{Code: code, Message: message, Err: err}
}

// statusCoder is implemented by errors that carry an HTTP status code (for
// example *models.Error). It lets ErrorCode map a platform response to a stable
// code without the output package importing the client packages.
type statusCoder interface{ HTTPStatus() int }

// ErrorCode returns the machine-readable code for err. An explicit CodedError
// code always wins; an error exposing an HTTP status is mapped by status;
// otherwise the message is classified heuristically. It returns "" for a nil
// error.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded *CodedError
	if errors.As(err, &coded) && coded.Code != "" {
		return coded.Code
	}
	var sc statusCoder
	if errors.As(err, &sc) {
		if code := httpStatusErrorCode(sc.HTTPStatus()); code != "" {
			return code
		}
	}
	return classifyError(err.Error())
}

// httpStatusErrorCode maps an HTTP status to a stable code, mirroring the
// per-command device mapping (see cmd.vmErrorCode / cmd.cliErrorCode). It
// returns "" for statuses with no specific code, which defers to the textual
// classifier.
func httpStatusErrorCode(status int) string {
	switch {
	case status == 401:
		return "AUTH_INVALID"
	case status == 403:
		return "FORBIDDEN"
	case status == 404:
		return "NOT_FOUND"
	case status >= 500:
		return "DEVICE_ERROR"
	default:
		return ""
	}
}

// NewStructuredError converts err into a StructuredError envelope.
func NewStructuredError(err error) StructuredError {
	if err == nil {
		return StructuredError{Error: ErrorDetail{Code: "OK"}}
	}
	code := ErrorCode(err)
	recoverable, suggestion := errorGuidance(code)
	return StructuredError{Error: ErrorDetail{
		Code:        code,
		Message:     err.Error(),
		Recoverable: recoverable,
		Suggestion:  suggestion,
	}}
}

// errorGuidance maps a stable error code to the agent-facing hint fields. A
// code whose remedy is to fix arguments, re-authenticate, or wait and retry is
// recoverable; a hard failure (forbidden, not found) is not. Unknown codes get
// no suggestion and are treated as non-recoverable.
func errorGuidance(code string) (bool, string) {
	switch code {
	case "AUTH_INVALID":
		return true, "Verify credentials with --username/--password or 'kvm-cli config set password'."
	case "NETWORK_ERROR":
		return true, "Retry the command; if it persists, check connectivity to the device."
	case "DEVICE_ERROR":
		return true, "The device rejected the request; check the device state and retry."
	case "PLANNER_REQUIRED":
		return true, "Pick the element yourself: run 'kvm-cli cua find --all' (or --text <t>), then " +
			"'kvm-cli cua click --index N' (or --id/--text). Or opt in with --planner auto " +
			"(config planner_model auto) to use a models-platform chat model."
	case "USAGE":
		return true, "Correct the invocation (see the command's --help) and retry."
	case "FORBIDDEN":
		return false, "The authenticated account is not permitted to perform this operation."
	case "NOT_FOUND":
		return false, "Check the resource name or path and try again."
	default:
		return false, ""
	}
}

// RenderError writes err to stderr. In JSON mode it emits a StructuredError
// envelope so agents get a consistent, parseable error shape; in all other
// modes it prints a human-readable "Error: <message>" line.
func RenderError(err error, opts Options) error {
	if err == nil {
		return nil
	}

	if opts.Mode == ModeJSON {
		out, merr := json.MarshalIndent(NewStructuredError(err), "", "  ")
		if merr != nil {
			return fmt.Errorf("json marshal error: %w", merr)
		}
		if _, werr := fmt.Fprintln(os.Stderr, string(out)); werr != nil {
			return fmt.Errorf("write error: %w", werr)
		}
		return nil
	}

	if _, werr := fmt.Fprintf(os.Stderr, "Error: %v\n", err); werr != nil {
		return fmt.Errorf("write error: %w", werr)
	}
	return nil
}

// classifyError maps an error message to a stable code using substring
// heuristics. Commands that need an exact code should return a CodedError.
func classifyError(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "401"),
		strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "invalid credentials"),
		strings.Contains(lower, "authentication required"),
		strings.Contains(lower, "auth required"):
		return "AUTH_INVALID"
	case strings.Contains(lower, "403"),
		strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "insufficient permission"):
		return "FORBIDDEN"
	case strings.Contains(lower, "404"),
		strings.Contains(lower, "not found"):
		return "NOT_FOUND"
	case strings.Contains(lower, "timeout"),
		strings.Contains(lower, "timed out"),
		strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "context canceled"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "broken pipe"),
		// Bare io.EOF (and "unexpected EOF") surface from a truncated response
		// or a dropped connection; they are transport failures, not user
		// errors, so they must map to the system/runtime code.
		strings.Contains(lower, "eof"):
		return "NETWORK_ERROR"
	case strings.Contains(lower, "usage"),
		strings.Contains(lower, "invalid argument"),
		strings.Contains(lower, "required flag"),
		strings.Contains(lower, "unknown flag"),
		strings.Contains(lower, "unknown command"),
		strings.Contains(lower, "accepts "),
		strings.Contains(lower, "requires "):
		return "USAGE"
	default:
		return "ERROR"
	}
}
