package operatoract

import "fmt"

// Each RFC 0012 2.3 refusal is its own type so callers can errors.As on it and
// decide whether asking again can help (only CancelledError and CardAbsentError
// can; piggy operator-sign groups them the same way).

func describe(what, detail string) string {
	if detail == "" {
		return "operator act: " + what
	}
	return fmt.Sprintf("operator act: %s: %s", what, detail)
}

// NoSuchKeyError: no card agent reachable from the socket serves the key.
type NoSuchKeyError struct{ Detail string }

func (e *NoSuchKeyError) Error() string { return describe("no such key", e.Detail) }

// RefusedPolicyError: the slot is not attested PIN-always and touch-always.
type RefusedPolicyError struct{ Detail string }

func (e *RefusedPolicyError) Error() string { return describe("refused by slot policy", e.Detail) }

// RefusedNamespaceError: the namespace is not registered for operator acts.
type RefusedNamespaceError struct{ Detail string }

func (e *RefusedNamespaceError) Error() string { return describe("refused namespace", e.Detail) }

// RefusedTextError: the text is not displayable.
type RefusedTextError struct{ Detail string }

func (e *RefusedTextError) Error() string { return describe("refused text", e.Detail) }

// CancelledError: the operator declined, dismissed the PIN prompt, or the
// touch timed out.
type CancelledError struct{ Detail string }

func (e *CancelledError) Error() string { return describe("cancelled", e.Detail) }

// CardAbsentError: the key is known but its card is not present.
type CardAbsentError struct{ Detail string }

func (e *CardAbsentError) Error() string { return describe("card absent", e.Detail) }

// BadRequestError: the agent judged the request malformed.
type BadRequestError struct{ Detail string }

func (e *BadRequestError) Error() string { return describe("bad request", e.Detail) }

// AgentError: status 8, any other failure; Detail says what (for example a
// wrong or blocked PIN).
type AgentError struct{ Detail string }

func (e *AgentError) Error() string { return describe("agent error", e.Detail) }

// UnknownStatusError: a status code above 8, reserved by the RFC; a failure.
type UnknownStatusError struct {
	Code   uint32
	Detail string
}

func (e *UnknownStatusError) Error() string {
	return describe(fmt.Sprintf("unknown status %d", e.Code), e.Detail)
}

// NotSupportedError: the agent does not implement operator-act@piggy at all
// (SSH_AGENT_FAILURE or SSH_AGENT_EXTENSION_FAILURE), as an agent older than
// the extension answers. Distinct from a refusal (RFC 0012 2.2).
type NotSupportedError struct{ Cause error }

func (e *NotSupportedError) Error() string {
	return fmt.Sprintf("operator act: the agent does not serve %s: %v", extensionName, e.Cause)
}

func (e *NotSupportedError) Unwrap() error { return e.Cause }

// ProtocolError: the agent answered, but not with a well-formed operator-act
// response.
type ProtocolError struct{ Reason string }

func (e *ProtocolError) Error() string {
	return "operator act: unexpected answer from the agent: " + e.Reason
}

// statusError maps an RFC 0012 2.3 status code to its error; nil for signed.
func statusError(code uint32, detail string) error {
	switch code {
	case 0:
		return nil
	case 1:
		return &NoSuchKeyError{Detail: detail}
	case 2:
		return &RefusedPolicyError{Detail: detail}
	case 3:
		return &RefusedNamespaceError{Detail: detail}
	case 4:
		return &RefusedTextError{Detail: detail}
	case 5:
		return &CancelledError{Detail: detail}
	case 6:
		return &CardAbsentError{Detail: detail}
	case 7:
		return &BadRequestError{Detail: detail}
	case 8:
		return &AgentError{Detail: detail}
	default:
		return &UnknownStatusError{Code: code, Detail: detail}
	}
}
