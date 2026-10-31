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

var notes = map[string]NotificationInfo{}

// clientNotes handle client→server notifications by method.
var clientNotes = map[string]func(s *Server, raw json.RawMessage){}

// serverNote declares a notification the server sends.
func serverNote[P any](name, doc string) {
	notes[name] = NotificationInfo{Name: name, Doc: doc, Params: reflect.TypeFor[P]()}
}

// clientNote registers a notification the client sends and its handler.
func clientNote[P any](name, doc string, fn func(s *Server, p P)) {
	notes[name] = NotificationInfo{Name: name, Doc: doc, Params: reflect.TypeFor[P](), FromClient: true}
	clientNotes[name] = func(s *Server, raw json.RawMessage) {
		var p P
		if json.Unmarshal(raw, &p) == nil {
			fn(s, p)
		}
	}
}

// Notifications lists every notification either side sends, by name.
func Notifications() []NotificationInfo {
	var out []NotificationInfo
	for _, n := range notes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func init() {
	serverNote[wire.ProgressParams]("$/progress", "One progress event of a long request.")
	serverNote[wire.SubEvent]("sub.event", "One update of a subscription.")
	clientNote("$/cancelRequest", "Cancel an in-flight request.", func(s *Server, p wire.CancelParams) {
		s.mu.Lock()
		if cancel := s.calls[string(p.ID)]; cancel != nil {
			cancel()
		}
		s.mu.Unlock()
	})
	clientNote("sub.cancel", "End a subscription.", func(s *Server, p wire.SubCancelParams) { s.cancelSub(p.Sub) })
	clientNote("client.activity", "Whether the client's window is focused, and what it shows.", func(s *Server, p wire.ClientActivity) {
		s.mu.Lock()
		s.activity = p
		s.mu.Unlock()
		s.overview.kick(0)
	})
}
