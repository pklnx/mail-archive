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
