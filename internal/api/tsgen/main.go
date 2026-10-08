// Command tsgen writes the TypeScript twin of the `pier api` protocol: every
// type in pkg/pier/wire that the method table reaches, the method, watch and
// notification maps, and the protocol version. The Mac app imports the
// result; CI regenerates it and fails on any difference, so the two sides
// can't drift. It is a build tool, never linked into pier.
//
//	go run ./internal/api/tsgen -o macos/src/api/protocol.gen.ts
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/usepier/pier/internal/api/tsgen/gen"
)

func main() {
	out := flag.String("o", "", "output file (default stdout)")
	wireDir := flag.String("wire", "pkg/pier/wire", "directory of the wire package sources (for docs and enum values)")
	flag.Parse()
	src, err := gen.Generate(*wireDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsgen:", err)
		os.Exit(1)
	}
	if *out == "" {
		os.Stdout.WriteString(src)
		return
	}
	if err := os.WriteFile(*out, []byte(src), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "tsgen:", err)
		os.Exit(1)
	}
}
