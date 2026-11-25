package confinement

import "context"

// Transport is the way a tool call reached this serve.
type Transport string

const (
	// TransportStdio is the call path that predates the bridge: the MCP client
	// on this serve's stdio. Its caller is classified from this serve's own
	// cgroup and the frontend records (ClassifyServeCaller).
	TransportStdio Transport = "stdio"
	// TransportBridge is a call that arrived on a bridge stream (bridge
	// contract v1). The listener accepted it only from a peer inside the
	// session's unit, so the caller is confined by construction.
	TransportBridge Transport = "bridge"
)

// CallerOrigin is who a tool call is made for, as the transport established
// it. For a bridged call the listener fills it from the socket it was created
// for and the peer's cgroup; nothing in a request can set or change it.
type CallerOrigin struct {
	Transport  Transport
	SessionKey string
	Unit       string
}

type originKey struct{}

// WithOrigin returns ctx carrying o. Only a transport that has itself
// established the caller calls it, before it hands a request to a tool.
func WithOrigin(ctx context.Context, o CallerOrigin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom is the origin ctx carries, and false when none was attached (every
// stdio call: the classification then falls back to the serve's own).
func OriginFrom(ctx context.Context) (CallerOrigin, bool) {
	if ctx == nil {
		return CallerOrigin{}, false
	}
	o, ok := ctx.Value(originKey{}).(CallerOrigin)
	return o, ok
}

// Bridged says the call arrived on a bridge stream.
func (o CallerOrigin) Bridged() bool { return o.Transport == TransportBridge }

// BridgedWhy is the reason a bridged call classifies as confined.
const BridgedWhy = "the call arrived over the bridge, from inside the session's unit"

// ClassifyOrigin answers "is the caller of this call confined?". A call that
// arrived on the bridge is confined by construction: the listener admitted only
// a peer in the session's unit, whatever this serve's own cgroup and records
// say, so the serve's classification is not consulted at all. Every other call
// (a stdio origin, or none) is classified by stdio, unchanged.
func ClassifyOrigin(ctx context.Context, stdio func() (bool, string)) (confined bool, why string) {
	if o, ok := OriginFrom(ctx); ok && o.Bridged() {
		return true, BridgedWhy
	}
	return stdio()
}
