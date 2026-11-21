package firc

import (
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// MaxTunnelFilterBytes matches fircd's tconfig.c limit ("a regular expression under 1024 bytes").
const MaxTunnelFilterBytes = 1023

// ValidateTunnelFilters trims t.Filter / t.FilterExclude and validates both regexes
// with Go RE2 syntax (providing clear lookaround hints via subscription.CompileMemberFilter)
// and fircd's byte-length limit.
func ValidateTunnelFilters(t *Tunnel) error {
	if t == nil {
		return nil
	}
	t.Filter = strings.TrimSpace(t.Filter)
	t.FilterExclude = strings.TrimSpace(t.FilterExclude)
	if len(t.Filter) > MaxTunnelFilterBytes {
		return &subscription.FilterError{
			Msg: fmt.Sprintf("фильтр «включать только»: длина превышает %d байт", MaxTunnelFilterBytes),
		}
	}
	if len(t.FilterExclude) > MaxTunnelFilterBytes {
		return &subscription.FilterError{
			Msg: fmt.Sprintf("фильтр «исключать»: длина превышает %d байт", MaxTunnelFilterBytes),
		}
	}
	_, err := subscription.CompileMemberFilter(t.Filter, t.FilterExclude)
	return err
}
