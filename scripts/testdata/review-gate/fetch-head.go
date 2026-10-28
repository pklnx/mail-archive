package fetch

import "net/http"

// Old is in the base commit already: the gate must not report it.
func Old(url string) (int, error) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// New is added by the head commit; the body is closed, so only noctx fires.
func New(url string) (int, error) {
	resp, err := http.Get(url) // want noctx
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func must(r *http.Request, err error) *http.Request {
	if err != nil {
		panic(err)
	}
	return r
}

// Pair has two findings on one line (golangci-lint keeps one per line unless
// --uniq-by-line=false).
func Pair(url string) (*http.Request, *http.Request) {
	return must(http.NewRequest(http.MethodGet, url, nil)), must(http.NewRequest(http.MethodHead, url, nil)) // want noctx noctx
}
