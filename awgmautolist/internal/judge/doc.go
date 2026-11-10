// Package judge implements the bounded, pure autolist state machine.
// It attributes transport failures to DNS-named hosts or their registerable
// domains, and keeps separate failed and bypass address observations.
//
// A Judge is not safe for concurrent use. Callers supply event timestamps;
// the package performs no I/O and reads no wall clock.
package judge
