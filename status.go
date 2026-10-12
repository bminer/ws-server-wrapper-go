package wrapper

// StatusCode represents a WebSocket close status code as defined in RFC 6455
// §7.4 (https://tools.ietf.org/html/rfc6455#section-7.4).
type StatusCode int

// Standard WebSocket close status codes.
const (
	StatusNormalClosure   StatusCode = 1000 // normal, intentional close
	StatusGoingAway       StatusCode = 1001 // server going down or browser navigating away
	StatusProtocolError   StatusCode = 1002 // protocol error
	StatusUnsupportedData StatusCode = 1003 // received data of unsupported type

	StatusInvalidFramePayloadData StatusCode = 1007 // invalid UTF-8 or data inconsistency
	StatusPolicyViolation         StatusCode = 1008 // message violates policy
	StatusMessageTooBig           StatusCode = 1009 // message too large to process
	StatusMandatoryExtension      StatusCode = 1010 // client required extension not negotiated
	StatusInternalError           StatusCode = 1011 // unexpected server condition
	StatusServiceRestart          StatusCode = 1012 // server restarting
	StatusTryAgainLater           StatusCode = 1013 // server temporarily unavailable
	StatusBadGateway              StatusCode = 1014 // gateway received invalid response
)
