package transport

import "fmt"

// HTTPError is returned by Client.Get / GetRaw / Post when NDMS replies
// with a non-2xx status. Typed so callers can match on Status — e.g.
// a 404 on /show/interface/<name>/wireguard/peer means "no peers",
// not a real error.
type HTTPError struct {
	Method string
	Path   string
	Status int
	Body   []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("rci %s %s: status %d", e.Method, e.Path, e.Status)
}
