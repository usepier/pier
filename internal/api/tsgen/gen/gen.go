// Package gen renders the `pier api` protocol as TypeScript.
package gen

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/usepier/pier/internal/api"
	"github.com/usepier/pier/pkg/pier/wire"
)

const wirePkg = "github.com/usepier/pier/pkg/pier/wire"

// source is what the Go reflection can't see: doc comments and the values
// of string enums, read from the wire package's files.
type source struct {
	typeDocs  map[string]string
	fieldDocs map[string]map[string]string
	enums     map[string][]enumValue
}

type enumValue struct{ value, doc string }

func parseWire(dir string) (*source, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	src := &source{typeDocs: map[string]string{}, fieldDocs: map[string]map[string]string{}, enums: map[string][]enumValue{}}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					switch sp := spec.(type) {
					case *ast.TypeSpec:
						doc := sp.Doc
						if doc == nil {
							doc = gd.Doc
						}
						src.typeDocs[sp.Name.Name] = text(doc)
						if st, ok := sp.Type.(*ast.StructType); ok {
							fd := map[string]string{}
							for _, fl := range st.Fields.List {
								d := text(fl.Doc)
								if d == "" {
									d = text(fl.Comment)
								}
								for _, n := range fl.Names {
									fd[n.Name] = d
								}
							}
							src.fieldDocs[sp.Name.Name] = fd
						}
					case *ast.ValueSpec:
						if gd.Tok != token.CONST || sp.Type == nil || len(sp.Values) != 1 {
							continue
						}
						id, ok := sp.Type.(*ast.Ident)
						lit, ok2 := sp.Values[0].(*ast.BasicLit)
						if !ok || !ok2 || lit.Kind != token.STRING {
							continue
						}
						v, _ := strconv.Unquote(lit.Value)
						src.enums[id.Name] = append(src.enums[id.Name], enumValue{v, text(sp.Comment)})
					}
				}
			}
		}
	}
	return src, nil
}

func text(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return strings.TrimSpace(cg.Text())
}

type writer struct {
	src     *source
	b       strings.Builder
	pending []reflect.Type
	seen    map[reflect.Type]bool
}

var (
	timeType = reflect.TypeFor[time.Time]()
	rawType  = reflect.TypeFor[json.RawMessage]()
	anyType  = reflect.TypeFor[any]()
)

// ref is the TypeScript for a Go type, queueing named wire types for
// declaration.
func (w *writer) ref(t reflect.Type) string {
	switch {
	case t == nil, t == anyType:
		return "unknown"
	case t == timeType:
		return "string"
	case t == rawType:
		return "number | string"
	}
	switch t.Kind() {
	case reflect.Pointer:
		if t.Elem() == reflect.TypeFor[wire.Empty]() {
			return "null"
		}
		return w.ref(t.Elem()) + " | null"
	case reflect.Slice:
		el := w.ref(t.Elem())
		if strings.Contains(el, " ") {
			el = "(" + el + ")"
		}
		return el + "[]"
	case reflect.Map:
		return "Record<string, " + w.ref(t.Elem()) + ">"
	case reflect.Interface:
		return "unknown"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	}
	if t.PkgPath() == wirePkg && t.Name() != "" {
		if strings.HasPrefix(t.Name(), "WatchResult[") {
			return "WatchResult<" + w.ref(t.Field(2).Type) + ">"
		}
		if !w.seen[t] {
			w.seen[t] = true
			w.pending = append(w.pending, t)
		}
		return t.Name()
	}
	if t.Kind() == reflect.String {
		return "string"
	}
	panic(fmt.Sprintf("tsgen: %v is not a wire type", t))
}

func (w *writer) doc(indent, d string) {
	if d == "" {
		return
	}
	lines := strings.Split(d, "\n")
	if len(lines) == 1 {
		fmt.Fprintf(&w.b, "%s/** %s */\n", indent, lines[0])
		return
	}
	fmt.Fprintf(&w.b, "%s/**\n", indent)
	for _, l := range lines {
		fmt.Fprintf(&w.b, "%s * %s\n", indent, strings.TrimRight(l, " "))
	}
	fmt.Fprintf(&w.b, "%s */\n", indent)
}

func (w *writer) declare(t reflect.Type) {
	name := t.Name()
	w.doc("", w.src.typeDocs[name])
	if t.Kind() == reflect.String {
		vals := w.src.enums[name]
		if len(vals) == 0 {
			fmt.Fprintf(&w.b, "export type %s = string;\n\n", name)
			return
		}
		var parts []string
		for _, v := range vals {
			parts = append(parts, strconv.Quote(v.value))
		}
		fmt.Fprintf(&w.b, "export type %s = %s;\n\n", name, strings.Join(parts, " | "))
		return
	}
	if t.Kind() != reflect.Struct {
		ts := "unknown"
		switch t.Kind() {
		case reflect.Bool:
			ts = "boolean"
		case reflect.Int, reflect.Int64, reflect.Float64:
			ts = "number"
		}
		fmt.Fprintf(&w.b, "export type %s = %s;\n\n", name, ts)
		return
	}
	if t.NumField() == 0 {
		fmt.Fprintf(&w.b, "export type %s = Record<string, never>;\n\n", name)
		return
	}
	var extends []string
	var body strings.Builder
	saved := w.b
	w.b = strings.Builder{}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			extends = append(extends, w.ref(f.Type))
			continue
		}
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		jname, opts, _ := strings.Cut(tag, ",")
		if jname == "-" {
			continue
		}
		if jname == "" {
			jname = f.Name
		}
		optional := strings.Contains(opts, "omitempty")
		ft := f.Type
		ts := w.ref(ft)
		if optional && ft.Kind() == reflect.Pointer {
			ts = strings.TrimSuffix(ts, " | null")
		}
		w.doc("  ", w.src.fieldDocs[name][f.Name])
		q := ""
		if optional {
			q = "?"
		}
		fmt.Fprintf(&w.b, "  %s%s: %s;\n", jname, q, ts)
	}
	body.WriteString(w.b.String())
	w.b = saved
	ext := ""
	if len(extends) > 0 {
		ext = " extends " + strings.Join(extends, ", ")
	}
	fmt.Fprintf(&w.b, "export interface %s%s {\n%s}\n\n", name, ext, body.String())
}

// Generate renders the protocol. wireDir holds the wire package's sources.
func Generate(wireDir string) (string, error) {
	src, err := parseWire(wireDir)
	if err != nil {
		return "", err
	}
	w := &writer{src: src, seen: map[reflect.Type]bool{}}
	var maps strings.Builder

	fmt.Fprintf(&maps, "export const PROTOCOL_VERSION = %d;\n\n", wire.ProtocolVersion)
	fmt.Fprintf(&maps, "/** Answer of a <topic>.watch: the subscription and its first data. */\n")
	fmt.Fprintf(&maps, "export interface WatchResult<T> {\n  sub: string;\n  seq: number;\n  snapshot: T;\n}\n\n")

	fmt.Fprintf(&maps, "/** Every request method: its params and result. */\nexport interface Methods {\n")
	var watches []api.MethodInfo
	for _, m := range api.Methods() {
		if m.Topic != "" {
			watches = append(watches, m)
		}
		w.doc("  ", m.Doc)
		fmt.Fprintf(&maps, "%s", w.b.String())
		w.b.Reset()
		fmt.Fprintf(&maps, "  %q: { params: %s; result: %s };\n", m.Name, w.ref(m.Params), w.ref(m.Result))
	}
	fmt.Fprintf(&maps, "}\n\n/** Every subscription topic: its watch params, its snapshot, and the data its events carry. */\nexport interface Watches {\n")
	for _, m := range watches {
		snap, _ := m.Result.FieldByName("Snapshot")
		fmt.Fprintf(&maps, "  %q: { params: %s; snapshot: %s; data: %s };\n", m.Topic, w.ref(m.Params), w.ref(snap.Type), w.ref(m.Data))
	}
	fmt.Fprintf(&maps, "}\n\n")
	for _, side := range []struct {
		name   string
		client bool
	}{{"ServerNotifications", false}, {"ClientNotifications", true}} {
		fmt.Fprintf(&maps, "export interface %s {\n", side.name)
		for _, n := range api.Notifications() {
			if n.FromClient != side.client {
				continue
			}
			w.doc("  ", n.Doc)
			fmt.Fprintf(&maps, "%s", w.b.String())
			w.b.Reset()
			fmt.Fprintf(&maps, "  %q: %s;\n", n.Name, w.ref(n.Params))
		}
		fmt.Fprintf(&maps, "}\n\n")
	}

	// Declare every reached type, including ones reached while declaring.
	var decls strings.Builder
	done := map[reflect.Type]bool{}
	for len(w.pending) > 0 {
		sort.Slice(w.pending, func(i, j int) bool { return w.pending[i].Name() < w.pending[j].Name() })
		t := w.pending[0]
		w.pending = w.pending[1:]
		if done[t] {
			continue
		}
		done[t] = true
		w.b.Reset()
		w.declare(t)
		decls.WriteString(w.b.String())
	}
	header := "// Code generated by internal/api/tsgen from pkg/pier/wire and the `pier api`\n" +
		"// method table. DO NOT EDIT: change the Go side and run `make api-types`.\n\n"
	return header + decls.String() + strings.TrimRight(maps.String(), "\n") + "\n", nil
}
