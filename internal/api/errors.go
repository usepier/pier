package api

import (
	"context"
	"errors"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/internal/driver"
	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    *wire.ErrorData `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

func rpcErr(code int, kind wire.ErrorKind, msg string) *rpcError {
	return &rpcError{Code: code, Message: msg, Data: &wire.ErrorData{Kind: kind, Message: msg}}
}

func invalidParams(msg string) error {
	return rpcErr(-32602, wire.ErrInvalidParams, "invalid params: "+msg)
}

// Error codes for pier's own failures, in the JSON-RPC server-error range.
const codeFailed = -32000

// kinds maps pier's sentinel errors to wire kinds. Order matters only in
// that more specific errors come first.
var kinds = []struct {
	err  error
	kind wire.ErrorKind
}{
	{config.ErrNoConfig, wire.ErrNotSetUp},
	{pier.ErrNotFound, wire.ErrNotFound},
	{pier.ErrAmbiguous, wire.ErrAmbiguous},
	{pier.ErrStillCreating, wire.ErrStillCreating},
	{pier.ErrCreateFailed, wire.ErrCreateFailed},
	{pier.ErrDeleting, wire.ErrDeleting},
	{pier.ErrParked, wire.ErrParked},
	{pier.ErrConflict, wire.ErrConflict},
	{pier.ErrNoCheckout, wire.ErrNoCheckout},
	{pier.ErrHeadMoved, wire.ErrHeadMoved},
	{pier.ErrNoPR, wire.ErrNotFound},
	{pier.ErrNoKey, wire.ErrNoKey},
	{pier.ErrNotInRepo, wire.ErrNotInRepo},
	{context.Canceled, wire.ErrCancelled},
}

// toRPCError classifies err for the client.
func toRPCError(err error) *rpcError {
	var re *rpcError
	if errors.As(err, &re) {
		return re
	}
	e := rpcErr(codeFailed, wire.ErrFailed, err.Error())
	var le *driver.LoginError
	if errors.As(err, &le) {
		e.Data.Kind, e.Data.Login = wire.ErrAuthExpired, le.Login
		return e
	}
	var se *pier.SetupError
	if errors.As(err, &se) {
		e.Data.Kind = map[string]wire.ErrorKind{"no_vpc": wire.ErrNoDefaultVPC, "billing": wire.ErrNoBilling, "permission": wire.ErrNoPermission}[se.Kind]
		if e.Data.Kind == "" {
			e.Data.Kind = wire.ErrFailed
		}
		return e
	}
	for _, k := range kinds {
		if errors.Is(err, k.err) {
			e.Data.Kind = k.kind
			return e
		}
	}
	return e
}

// errorData is toRPCError's data, for errors reported inside results (a
// failed refresh in an overview) rather than as the call's error.
func errorData(err error) *wire.ErrorData { return toRPCError(err).Data }
