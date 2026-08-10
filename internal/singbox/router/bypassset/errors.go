package bypassset

import "errors"

// ErrOpkgNotFound is returned when opkg is not present on the router.
var ErrOpkgNotFound = errors.New("opkg not found: cannot install ipset without a package manager")

// ErrIPSetNotAvailable is returned when ipset binary is absent and the
// caller cannot proceed without it.
var ErrIPSetNotAvailable = errors.New("ipset binary not found: install the ipset package via opkg")
