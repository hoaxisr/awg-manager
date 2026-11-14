// Package buildinfo contains the product and package version.
package buildinfo

// Version is set by build-ipk.sh via ldflags to the package version, which
// equals the awg-manager version.
var Version = "dev"
