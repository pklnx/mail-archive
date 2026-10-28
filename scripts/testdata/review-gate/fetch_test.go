package fetch

import (
	"net/http"
	"testing"
)

// Test code is out of scope for the gate's noctx check.
func TestFetch(t *testing.T) {
	resp, err := http.Get("http://127.0.0.1:1/")
	if err == nil {
		resp.Body.Close()
	}
}
