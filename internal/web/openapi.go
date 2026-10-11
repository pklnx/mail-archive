package web

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// openAPIYAML describes the JSON API. It is written by hand; a test compares
// it with the route table.
//
//go:embed openapi.yaml
var openAPIYAML []byte

const openAPIPath = "/api/openapi.json"

type openAPIDoc struct {
	body []byte
	etag string
}

// openAPI converts the spec once. An error is a programming error that the
// tests catch; ListenAndServe refuses to start with it.
var openAPI = sync.OnceValues(func() (openAPIDoc, error) {
	body, err := yamlToJSON(openAPIYAML)
	if err != nil {
		return openAPIDoc{}, fmt.Errorf("openapi.yaml: %w", err)
	}
	sum := sha256.Sum256(body)
	return openAPIDoc{body: body, etag: `"` + hex.EncodeToString(sum[:]) + `"`}, nil
})

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	doc, err := openAPI()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", doc.etag)
	// ServeContent answers If-None-Match with 304.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(doc.body))
}

// yamlToJSON converts a YAML document to JSON. It walks the node tree
// instead of decoding into any: that would turn an unquoted mapping key such
// as a response code (404:) into a non-string key, which encoding/json
// refuses, and an unquoted date into a time.Time.
func yamlToJSON(src []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	v, err := yamlValue(&doc)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func yamlValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return nil, errors.New("empty document")
		}
		return yamlValue(n.Content[0])
	case yaml.AliasNode:
		return yamlValue(n.Alias)
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.AliasNode {
				k = k.Alias
			}
			if k.Kind != yaml.ScalarNode || k.ShortTag() == "!!merge" {
				return nil, fmt.Errorf("line %d: only scalar mapping keys are supported", k.Line)
			}
			if _, dup := m[k.Value]; dup {
				return nil, fmt.Errorf("line %d: duplicate key %q", k.Line, k.Value)
			}
			v, err := yamlValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[k.Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		s := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := yamlValue(c)
			if err != nil {
				return nil, err
			}
			s = append(s, v)
		}
		return s, nil
	case yaml.ScalarNode:
		return yamlScalar(n)
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", n.Line)
}

func yamlScalar(n *yaml.Node) (any, error) {
	var err error
	switch n.ShortTag() {
	case "!!str", "!!timestamp":
		// A date stays the string that was written.
		return n.Value, nil
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		err = n.Decode(&b)
		return b, err
	case "!!int":
		var i int64
		err = n.Decode(&i)
		return i, err
	case "!!float":
		var f float64
		err = n.Decode(&f)
		return f, err
	}
	return nil, fmt.Errorf("line %d: unsupported YAML tag %s", n.Line, n.ShortTag())
}
