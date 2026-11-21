package transport

// ErrorNotifier is implemented by transports that want to signal
// out-of-band conditions (captcha, login required) to the layer above.
//
// SetErrorNotifier is called once by the manager before Start. The callback
// must not block; it runs on the transport's reconnect goroutine.
//
// transportName and url identify which transport reported the error, so the
// manager can route it to the right IPC channel. html is an alternative to
// url for a transport that wants the operator to see a page it built itself
// rather than a real site ("" for every native transport, which only ever
// point at a real URL - see transport/script, the one implementation that
// uses it).
type ErrorNotifier interface {
	SetErrorNotifier(func(err error, transportName, url, html, reason string))
}
