//go:build ignore

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type routeLocation struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type routeRecord struct {
	Path      string          `json:"path"`
	Locations []routeLocation `json:"locations"`
}

type routeInventory struct {
	GeneratedBy string        `json:"generatedBy"`
	Routes      []routeRecord `json:"routes"`
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	routes, err := collectRoutes(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(routeInventory{
		GeneratedBy: "scripts/web_api_route_inventory.go",
		Routes:      routes,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func collectRoutes(root string) ([]routeRecord, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	serverRoot := filepath.Join(absoluteRoot, "internal", "server")
	fset := token.NewFileSet()
	locations := map[string][]routeLocation{}
	err = filepath.WalkDir(serverRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			pattern, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil {
				return true
			}
			pattern = serveMuxRoutePath(pattern)
			if pattern == "" {
				return true
			}
			position := fset.Position(literal.Pos())
			relative, relErr := filepath.Rel(absoluteRoot, position.Filename)
			if relErr != nil {
				relative = position.Filename
			}
			locations[pattern] = append(locations[pattern], routeLocation{
				File: filepath.ToSlash(relative),
				Line: position.Line,
			})
			return true
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan Go server routes: %w", err)
	}

	paths := make([]string, 0, len(locations))
	for path := range locations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	records := make([]routeRecord, 0, len(paths))
	for _, path := range paths {
		sort.Slice(locations[path], func(i, j int) bool {
			if locations[path][i].File != locations[path][j].File {
				return locations[path][i].File < locations[path][j].File
			}
			return locations[path][i].Line < locations[path][j].Line
		})
		records = append(records, routeRecord{Path: path, Locations: locations[path]})
	}
	return records, nil
}

// serveMuxRoutePath accepts both the historical path-only registration and
// Go 1.22 method-qualified patterns such as "GET /api/kernels". The HTTP
// method narrows dispatch but does not change the frontend path contract.
func serveMuxRoutePath(pattern string) string {
	fields := strings.Fields(strings.TrimSpace(pattern))
	if len(fields) == 1 && strings.HasPrefix(fields[0], "/") {
		return fields[0]
	}
	if len(fields) == 2 && strings.HasPrefix(fields[1], "/") {
		switch fields[0] {
		case "CONNECT", "DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT", "TRACE":
			return fields[1]
		}
	}
	return ""
}
