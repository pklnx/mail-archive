package web

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// specMethods are the operations of a path item that can match a route.
var specMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

// expectedSecurity is the security an operation must declare for a route's
// access: read and sync routes name the API key scope they need.
func expectedSecurity(a access) any {
	session := map[string]any{"session": []any{}}
	switch a {
	case accessPublic, accessLogin:
		return []any{}
	case accessRead, accessSync:
		return []any{session, map[string]any{"apiKey": []any{requiredScope(a)}}}
	default:
		return []any{session}
	}
}

// compareSpec lists the differences between the route table and an OpenAPI
// document decoded from JSON.
func compareSpec(routes []route, spec map[string]any) []string {
	var problems []string
	paths, _ := spec["paths"].(map[string]any)
	inTable := make(map[string]bool)
	opIDs := make(map[string]string)
	for _, rt := range routes {
		if rt.ui {
			continue
		}
		method, path, _ := strings.Cut(rt.pattern, " ")
		inTable[strings.ToLower(method)+" "+path] = true
		item, _ := paths[path].(map[string]any)
		op, ok := item[strings.ToLower(method)].(map[string]any)
		if !ok {
			problems = append(problems, "no operation in openapi.yaml for "+rt.pattern)
			continue
		}
		sec, ok := op["security"]
		if !ok {
			sec = spec["security"]
		}
		if want := expectedSecurity(rt.access); !reflect.DeepEqual(sec, want) {
			problems = append(problems, fmt.Sprintf("%s: security is %v, want %v", rt.pattern, sec, want))
		}
		declared := make(map[string]bool)
		for _, list := range []any{item["parameters"], op["parameters"]} {
			params, _ := list.([]any)
			for _, p := range params {
				if pm := resolveParam(spec, p); pm["in"] == "path" {
					name, _ := pm["name"].(string)
					declared[name] = true
				}
			}
		}
		for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
			if !declared[m[1]] {
				problems = append(problems, fmt.Sprintf("%s: no path parameter %q", rt.pattern, m[1]))
			}
			delete(declared, m[1])
		}
		for name := range declared {
			problems = append(problems, fmt.Sprintf("%s: path parameter %q is not in the path", rt.pattern, name))
		}
		id, _ := op["operationId"].(string)
		switch other, dup := opIDs[id]; {
		case id == "":
			problems = append(problems, rt.pattern+": no operationId")
		case dup:
			problems = append(problems, fmt.Sprintf("%s: operationId %q is also used by %s", rt.pattern, id, other))
		default:
			opIDs[id] = rt.pattern
		}
	}
	for path, item := range paths {
		item, _ := item.(map[string]any)
		for _, m := range specMethods {
			if _, ok := item[m]; ok && !inTable[m+" "+path] {
				problems = append(problems, fmt.Sprintf("openapi.yaml has %s %s, but no route does", strings.ToUpper(m), path))
			}
		}
	}
	slices.Sort(problems)
	return problems
}

// resolveParam follows a "#/components/parameters/..." reference.
func resolveParam(spec map[string]any, p any) map[string]any {
	pm, _ := p.(map[string]any)
	ref, ok := pm["$ref"].(string)
	if !ok {
		return pm
	}
	name, ok := strings.CutPrefix(ref, "#/components/parameters/")
	if !ok {
		return nil
	}
	components, _ := spec["components"].(map[string]any)
	params, _ := components["parameters"].(map[string]any)
	pm, _ = params[name].(map[string]any)
	return pm
}

func servedSpec(t *testing.T) map[string]any {
	t.Helper()
	doc, err := openAPI()
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(doc.body, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

// TestOpenAPIRoutes checks that openapi.yaml describes exactly the routes of
// the route table, with the security their access requires.
func TestOpenAPIRoutes(t *testing.T) {
	routes := (&Server{}).routes()
	for _, p := range compareSpec(routes, servedSpec(t)) {
		t.Error(p)
	}
	var ui []string
	for _, rt := range routes {
		if rt.ui {
			ui = append(ui, rt.pattern)
		}
	}
	if want := []string{"GET /{$}", "GET /assets/"}; !slices.Equal(ui, want) {
		t.Errorf("routes left out of the spec: %q, want %q", ui, want)
	}
}

func TestCompareSpec(t *testing.T) {
	session := []any{map[string]any{"session": []any{}}}
	param := []any{map[string]any{"name": "id", "in": "path"}}
	spec := func(paths map[string]any) map[string]any {
		return map[string]any{"security": session, "paths": paths}
	}
	op := func(id string) map[string]any { return map[string]any{"operationId": id} }
	routes := []route{
		{pattern: "GET /api/things"},
		{pattern: "DELETE /api/things/{id}"},
		{pattern: "GET /healthz", access: accessPublic},
		{pattern: "GET /{$}", access: accessPublic, ui: true},
	}
	good := func() map[string]any {
		return map[string]any{
			"/api/things":      map[string]any{"get": op("list")},
			"/api/things/{id}": map[string]any{"parameters": param, "delete": op("remove")},
			"/healthz":         map[string]any{"get": map[string]any{"operationId": "health", "security": []any{}}},
		}
	}
	for _, c := range []struct {
		name   string
		change func(paths map[string]any)
		want   string
	}{
		{"complete", func(map[string]any) {}, ""},
		{"route without operation", func(p map[string]any) { delete(p, "/api/things") },
			"no operation in openapi.yaml for GET /api/things"},
		{"operation without route", func(p map[string]any) { p["/api/other"] = map[string]any{"post": op("other")} },
			"openapi.yaml has POST /api/other, but no route does"},
		{"public route without security override", func(p map[string]any) { p["/healthz"] = map[string]any{"get": op("health")} },
			"GET /healthz: security is"},
		{"session route declared public", func(p map[string]any) {
			p["/api/things"] = map[string]any{"get": map[string]any{"operationId": "list", "security": []any{}}}
		}, "GET /api/things: security is"},
		{"missing path parameter", func(p map[string]any) { p["/api/things/{id}"] = map[string]any{"delete": op("remove")} },
			`DELETE /api/things/{id}: no path parameter "id"`},
		{"duplicate operationId", func(p map[string]any) { p["/api/things/{id}"].(map[string]any)["delete"] = op("list") },
			`operationId "list" is also used by`},
	} {
		t.Run(c.name, func(t *testing.T) {
			paths := good()
			c.change(paths)
			got := strings.Join(compareSpec(routes, spec(paths)), "\n")
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
	// A referenced path parameter counts.
	paths := good()
	paths["/api/things/{id}"].(map[string]any)["parameters"] = []any{map[string]any{"$ref": "#/components/parameters/id"}}
	s := spec(paths)
	s["components"] = map[string]any{"parameters": map[string]any{"id": param[0]}}
	if got := compareSpec(routes, s); len(got) > 0 {
		t.Errorf("with a $ref: %q", got)
	}
}

func TestYAMLToJSON(t *testing.T) {
	src := `
responses:
  "200": ok
  404: missing
date: 2026-10-11
flag: true
count: 3
ratio: 0.5
nothing: null
list: [a, 1]
base: &b {x: 1}
copy: *b
`
	got, err := yamlToJSON([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var gotV, wantV any
	want := `{"responses": {"200": "ok", "404": "missing"}, "date": "2026-10-11", "flag": true, "count": 3,
		"ratio": 0.5, "nothing": null, "list": ["a", 1], "base": {"x": 1}, "copy": {"x": 1}}`
	if err := json.Unmarshal(got, &gotV); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Errorf("got %s", got)
	}

	for _, bad := range []string{"a: 1\na: 2\n", "<<: {a: 1}\n", "? [a]\n: 1\n", "x: .nan\n"} {
		if _, err := yamlToJSON([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// TestServeOpenAPI checks that the spec is public and answers conditional
// requests.
func TestServeOpenAPI(t *testing.T) {
	h := New(nil, nil, slog.New(slog.DiscardHandler), Options{}).Handler()
	get := func(etag string) *http.Response {
		req := httptest.NewRequest(http.MethodGet, openAPIPath, nil)
		req.Host = "localhost"
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}
	resp := get("")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" ||
		resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	var served map[string]any
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(served, servedSpec(t)) || served["openapi"] != "3.1.0" {
		t.Error("served JSON differs from the converted YAML")
	}
	etag := resp.Header.Get("ETag")
	if len(etag) != 66 {
		t.Fatalf("ETag %q", etag)
	}
	if resp := get(etag); resp.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", resp.StatusCode)
	}
}

// TestAuthenticateByRoute checks the route table's access without a
// database: requests without a session never reach the store.
func TestAuthenticateByRoute(t *testing.T) {
	h := New(nil, nil, slog.New(slog.DiscardHandler), Options{}).Handler()
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/healthz", 200},
		{"GET", openAPIPath, 200},
		{"GET", "/api/status", 401},
		{"GET", "/api/messages/" + strings.Repeat("a", 64), 401},
		{"POST", "/api/sync", 401},
		{"GET", "/api/users", 401},
		{"GET", "/api/does-not-exist", 401},
		// A login path with a method no route takes is not public.
		{"PUT", "/api/session", 401},
		{"POST", openAPIPath, 401},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Host = "localhost"
		req.Header.Set("Origin", "http://localhost")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}
