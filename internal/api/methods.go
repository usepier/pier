package api

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/usepier/pier/pkg/pier/wire"
)

// method is one entry of the protocol's method table.
type method struct {
	Name   string
	Doc    string
	Params reflect.Type
	Result reflect.Type
	// Topic is set for subscriptions: the method is <topic>.watch and its
	// sub.event data has type Data.
	Topic string
	Data  reflect.Type
	run   func(ctx context.Context, s *Server, c *Call, raw json.RawMessage) (any, error)
}

var methods = map[string]*method{}

// handle registers a request handler. P and R define the wire shapes.
func handle[P, R any](name, doc string, fn func(ctx context.Context, s *Server, c *Call, p P) (R, error)) {
	methods[name] = &method{
		Name: name, Doc: doc,
		Params: reflect.TypeFor[P](), Result: reflect.TypeFor[R](),
		run: func(ctx context.Context, s *Server, c *Call, raw json.RawMessage) (any, error) {
			var p P
			if len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, invalidParams(err.Error())
				}
			}
			return fn(ctx, s, c, p)
		},
	}
}

// MethodInfo describes one method for code generation.
type MethodInfo struct {
	Name, Doc      string
	Params, Result reflect.Type
	Topic          string       // non-empty for <topic>.watch
	Data           reflect.Type // sub.event data of a watch
}

// Methods is the method table, sorted by name.
func Methods() []MethodInfo {
	var out []MethodInfo
	for _, m := range methods {
		out = append(out, MethodInfo{Name: m.Name, Doc: m.Doc, Params: m.Params, Result: m.Result, Topic: m.Topic, Data: m.Data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NotificationInfo describes one notification for code generation.
type NotificationInfo struct {
	Name, Doc string
	Params    reflect.Type
	// FromClient marks client→server notifications.
	FromClient bool
}

// Notifications lists every notification either side sends.
func Notifications() []NotificationInfo {
	return []NotificationInfo{
		{Name: "$/progress", Doc: "One progress event of a long request.", Params: reflect.TypeFor[wire.ProgressParams]()},
		{Name: "sub.event", Doc: "One update of a subscription.", Params: reflect.TypeFor[wire.SubEvent]()},
		{Name: "$/cancelRequest", Doc: "Cancel an in-flight request.", Params: reflect.TypeFor[wire.CancelParams](), FromClient: true},
		{Name: "sub.cancel", Doc: "End a subscription.", Params: reflect.TypeFor[wire.SubCancelParams](), FromClient: true},
		{Name: "client.activity", Doc: "Whether the client's window is focused, and what it shows.", Params: reflect.TypeFor[wire.ClientActivity](), FromClient: true},
	}
}
