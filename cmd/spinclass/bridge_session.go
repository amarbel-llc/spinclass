package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/jsonrpc"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/purse-first/libs/go-mcp/transport"

	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// bridgeSessionToolNames are the only tools the session service lists and
// serves, in the order it lists them. Their definitions are today's: they come
// from the registry serve already built for stdio.
var bridgeSessionToolNames = []string{
	"whoami",
	"list-handles",
	"restart-this-session",
	"cancel-restart",
	"close-this-session",
	"session-health",
	"update-this-session-description",
}

// bridgeCodeRefused is the JSON-RPC error code of a refusal (bridge contract
// v1, "Refusal text").
const bridgeCodeRefused = -32001

const unknownToolPrefix = "unknown tool: "

// bridgeSessionHandler serves the session service: MCP JSON-RPC 2.0,
// newline-delimited, over a bridge stream, through go-mcp's server like stdio
// MCP. The caller is the listener's (Caller), put into the context of every
// call as a bridge origin, so the existing gates classify it as confined and
// the identity verbs act for the listener's session; the request never names
// either.
type bridgeSessionHandler struct {
	tools   bridgeSessionProvider
	name    string
	version string
}

// crossingRecorder is where a served call is recorded (the crossing log). Nil
// records nothing.
type crossingRecorder interface {
	Record(crossinglog.Crossing)
}

// newBridgeSessionHandler serves the seven session tools out of registry and
// records each tools/call in rec.
func newBridgeSessionHandler(registry *server.ToolRegistryV1, name, version string, rec crossingRecorder) bridgeserver.SessionHandler {
	return &bridgeSessionHandler{tools: bridgeSessionProvider{registry: registry, rec: rec}, name: name, version: version}
}

// Serve runs one MCP server over conn until the peer ends the stream or ctx
// ends, and returns only when every in-flight call has finished.
func (h *bridgeSessionHandler) Serve(ctx context.Context, conn net.Conn, caller bridgeserver.Caller) {
	ctx = confinement.WithOrigin(ctx, confinement.CallerOrigin{
		Transport:  confinement.TransportBridge,
		SessionKey: caller.SessionKey,
		Unit:       caller.Unit,
	})
	// The listener closes conn after Serve returns, so the transport has no
	// closer of its own.
	t := bridgeErrorCodes{Transport: transport.NewStdio(conn, conn)}
	tools := h.tools
	tools.session = caller.SessionKey
	srv, err := server.New(t, server.Options{
		ServerName:    h.name,
		ServerVersion: h.version,
		Tools:         tools,
	})
	if err != nil {
		return
	}
	_ = srv.Run(ctx)
}

// bridgeSessionProvider is the registry restricted to the session tools.
type bridgeSessionProvider struct {
	registry *server.ToolRegistryV1
	rec      crossingRecorder
	session  string
}

func (p bridgeSessionProvider) allowed(name string) bool {
	for _, n := range bridgeSessionToolNames {
		if n == name {
			return true
		}
	}
	return false
}

func (p bridgeSessionProvider) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	all, err := p.registry.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]protocol.Tool{}
	for _, tool := range all {
		byName[tool.Name] = tool
	}
	var out []protocol.Tool
	for _, n := range bridgeSessionToolNames {
		if tool, ok := byName[n]; ok {
			out = append(out, tool)
		}
	}
	return out, nil
}

func (p bridgeSessionProvider) ListToolsV1(ctx context.Context, cursor string) (*protocol.ToolsListResultV1, error) {
	all, err := p.registry.ListToolsV1(ctx, cursor)
	if err != nil {
		return nil, err
	}
	byName := map[string]protocol.ToolV1{}
	for _, tool := range all.Tools {
		byName[tool.Name] = tool
	}
	out := &protocol.ToolsListResultV1{}
	for _, n := range bridgeSessionToolNames {
		if tool, ok := byName[n]; ok {
			out.Tools = append(out.Tools, tool)
		}
	}
	return out, nil
}

func (p bridgeSessionProvider) CallTool(ctx context.Context, name string, args json.RawMessage) (*protocol.ToolCallResult, error) {
	started := time.Now()
	res, err := p.callTool(ctx, name, args)
	var isErr bool
	if res != nil {
		isErr = res.IsError
	}
	p.record(name, args, started, res, isErr, err)
	return res, err
}

func (p bridgeSessionProvider) callTool(ctx context.Context, name string, args json.RawMessage) (*protocol.ToolCallResult, error) {
	if !p.allowed(name) {
		return nil, errors.New(unknownToolPrefix + name)
	}
	res, err := p.registry.CallTool(ctx, name, bridgeArgs(args))
	if err != nil {
		return nil, err
	}
	if len(res.Content) > 0 && res.IsError && strings.HasPrefix(res.Content[0].Text, bridgeRefusalPrefix) {
		return nil, errors.New(res.Content[0].Text)
	}
	return res, nil
}

func (p bridgeSessionProvider) CallToolV1(ctx context.Context, name string, args json.RawMessage) (*protocol.ToolCallResultV1, error) {
	started := time.Now()
	res, err := p.callToolV1(ctx, name, args)
	var isErr bool
	if res != nil {
		isErr = res.IsError
	}
	p.record(name, args, started, res, isErr, err)
	return res, err
}

func (p bridgeSessionProvider) callToolV1(ctx context.Context, name string, args json.RawMessage) (*protocol.ToolCallResultV1, error) {
	if !p.allowed(name) {
		return nil, errors.New(unknownToolPrefix + name)
	}
	res, err := p.registry.CallToolV1(ctx, name, bridgeArgs(args))
	if err != nil {
		return nil, err
	}
	if len(res.Content) > 0 && res.IsError && strings.HasPrefix(res.Content[0].Text, bridgeRefusalPrefix) {
		return nil, errors.New(res.Content[0].Text)
	}
	return res, nil
}

// record writes the crossing-log line of one tools/call. The verb is the tool
// name only when it is one of the seven, so a request's own text never names a
// line; the outcome follows the JSON-RPC result (a refusal is -32001, any
// other error or an isError result is "error"). The log redacts the arguments
// itself; nothing here is written that the log would not. A nil recorder
// records nothing, and the recorder never fails the call.
func (p bridgeSessionProvider) record(name string, args json.RawMessage, started time.Time, res any, isErr bool, err error) {
	if p.rec == nil {
		return
	}
	verb := "unknown"
	if p.allowed(name) {
		verb = name
	}
	outcome := crossinglog.OutcomeOK
	resp := 0
	switch {
	case err != nil:
		outcome = crossinglog.OutcomeError
		if strings.HasPrefix(err.Error(), bridgeRefusalPrefix) {
			outcome = crossinglog.OutcomeRefused
		}
		resp = len(err.Error())
	case isErr:
		outcome = crossinglog.OutcomeError
	}
	if err == nil && res != nil {
		if b, merr := json.Marshal(res); merr == nil {
			resp = len(b)
		}
	}
	p.rec.Record(crossinglog.Crossing{
		Session:   p.session,
		Service:   bridgeserver.ServiceSession,
		Verb:      verb,
		Outcome:   outcome,
		Duration:  time.Since(started),
		ReqBytes:  len(args),
		RespBytes: resp,
		Args:      args,
	})
}

// bridgeArgs gives a call that sent no arguments the empty object, which is
// what an MCP client sends for a tool without parameters.
func bridgeArgs(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage(`{}`)
	}
	return args
}

// bridgeErrorCodes gives the errors the provider raises their contract codes:
// go-mcp answers every provider error with InternalError, so a refusal (text
// opening with the contract's "refused: ") goes out as -32001 and an unknown
// tool as InvalidParams, as MCP says.
type bridgeErrorCodes struct {
	transport.Transport
}

func (t bridgeErrorCodes) Write(m *jsonrpc.Message) error {
	if m != nil && m.Error != nil && m.Error.Code == jsonrpc.InternalError {
		switch {
		case strings.HasPrefix(m.Error.Message, bridgeRefusalPrefix):
			m.Error.Code = bridgeCodeRefused
		case strings.HasPrefix(m.Error.Message, unknownToolPrefix):
			m.Error.Code = jsonrpc.InvalidParams
		}
	}
	return t.Transport.Write(m)
}
