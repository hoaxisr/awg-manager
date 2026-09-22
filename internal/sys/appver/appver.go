// Package appver holds the User-Agent that the daemon sends to hosts it is
// allowed to identify itself to: the router (RCI, /auth) and our own update
// and mirror hosts. Requests to addresses the user supplied stay anonymous,
// and requests that deliberately impersonate someone else (curl for IP-check
// services, client profiles for subscriptions) keep their own header.
package appver

// ua is process-wide by design: the version arrives via ldflags into package
// main and would otherwise have to be threaded through every HTTP client
// constructor and its tests. Written once by Set at startup; readers get a
// copy through UA(), so nothing outside this package can reassign it.
var ua = "awgm/dev"

// Set builds the User-Agent from the build-time version and arch key,
// e.g. "awgm/2.19.7 (mipsel-3.4)". Call once from main before serving.
func Set(version, arch string) {
	ua = "awgm/" + version
	if arch != "" {
		ua += " (" + arch + ")"
	}
}

// UA returns the User-Agent to send.
func UA() string { return ua }
