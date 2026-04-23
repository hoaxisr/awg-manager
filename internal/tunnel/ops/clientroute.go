package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// clientRouteOps implements the per-client policy-routing operations:
// setting up a dedicated routing table with a default-via-tunnel route,
// adding/removing `ip rule` policy entries that steer a client IP into
// that table, and cleaning up both when a client route is deleted.
//
// OS4 and OS5 share this logic verbatim — these are pure Linux kernel
// networking operations, not NDMS-managed ones, so the concrete
// operator type (kernel amneziawg vs NDMS OpkgTun) is irrelevant at
// this layer. The only per-operator variability is how `ip` is run:
// OS5 uses a mockable ipRunFunc, OS4 uses exec.Run directly. Both plug
// into the `run` field below.
type clientRouteOps struct {
	run     ipRunFunc
	logWarn func(action, target, msg string)
}

// newClientRouteOps constructs a clientRouteOps with the given `ip`
// runner and warn-logger. Both must be non-nil — callers pass the
// operator's ipRun field (OS5) or a thin adapter over exec.Run (OS4),
// and the operator's logWarn method value.
func newClientRouteOps(run ipRunFunc, logWarn func(string, string, string)) *clientRouteOps {
	return &clientRouteOps{run: run, logWarn: logWarn}
}

// SetupClientRouteTable sets up a routing table with a default route
// through the tunnel and a LAN bypass route so local traffic is not
// affected.
func (c *clientRouteOps) SetupClientRouteTable(ctx context.Context, kernelIface string, tableNum int) error {
	tableStr := strconv.Itoa(tableNum)

	result, err := c.run(ctx, "/opt/sbin/ip", "route", "replace", "default", "dev", kernelIface, "table", tableStr)
	if err != nil {
		return fmt.Errorf("setup route table %d: default route: %w", tableNum, exec.FormatError(result, err))
	}

	lanSubnet := c.detectLANSubnet(ctx)
	if lanSubnet != "" {
		result, err := c.run(ctx, "/opt/sbin/ip", "route", "replace", lanSubnet, "dev", "br0", "table", tableStr)
		if err != nil {
			c.logWarn("client-route", kernelIface, fmt.Sprintf("Failed to add LAN route %s to table %d: %s", lanSubnet, tableNum, exec.FormatError(result, err)))
		}
	}

	return nil
}

// AddClientRule adds an ip rule routing traffic from clientIP through
// the given table. Removes any existing rule for the same pair first,
// so the call is idempotent.
func (c *clientRouteOps) AddClientRule(ctx context.Context, clientIP string, tableNum int) error {
	tableStr := strconv.Itoa(tableNum)

	// Best-effort idempotent teardown — ignore error.
	c.run(ctx, "/opt/sbin/ip", "rule", "del", "from", clientIP, "lookup", tableStr)

	result, err := c.run(ctx, "/opt/sbin/ip", "rule", "add", "from", clientIP, "lookup", tableStr, "priority", tableStr)
	if err != nil {
		return fmt.Errorf("add client rule from %s lookup %d: %w", clientIP, tableNum, exec.FormatError(result, err))
	}
	return nil
}

// RemoveClientRule removes the ip rule for a client IP.
func (c *clientRouteOps) RemoveClientRule(ctx context.Context, clientIP string, tableNum int) error {
	tableStr := strconv.Itoa(tableNum)

	result, err := c.run(ctx, "/opt/sbin/ip", "rule", "del", "from", clientIP, "lookup", tableStr)
	if err != nil {
		return fmt.Errorf("remove client rule from %s lookup %d: %w", clientIP, tableNum, exec.FormatError(result, err))
	}
	return nil
}

// CleanupClientRouteTable flushes all routes in the table and removes
// every ip rule referencing it.
func (c *clientRouteOps) CleanupClientRouteTable(ctx context.Context, tableNum int) error {
	tableStr := strconv.Itoa(tableNum)

	result, err := c.run(ctx, "/opt/sbin/ip", "route", "flush", "table", tableStr)
	if err != nil {
		c.logWarn("client-route", tableStr, fmt.Sprintf("Failed to flush table: %s", exec.FormatError(result, err)))
	}

	// Remove all rules referencing this table (loop until no more).
	for i := 0; i < 100; i++ {
		_, err := c.run(ctx, "/opt/sbin/ip", "rule", "del", "lookup", tableStr)
		if err != nil {
			break // no more rules
		}
	}

	return nil
}

// ListUsedRoutingTables parses `ip rule list` output and returns
// unique table numbers.
func (c *clientRouteOps) ListUsedRoutingTables(ctx context.Context) ([]int, error) {
	result, err := c.run(ctx, "/opt/sbin/ip", "rule", "list")
	if err != nil {
		return nil, fmt.Errorf("ip rule list: %w", exec.FormatError(result, err))
	}
	return parseRoutingTables(result.Stdout), nil
}

// detectLANSubnet detects the LAN subnet from br0 interface routes.
// Returns "" if detection fails; logs a warn for visibility since a
// missing LAN bypass route silently routes LAN traffic through the
// tunnel, which is the kind of thing users need to see in logs.
func (c *clientRouteOps) detectLANSubnet(ctx context.Context) string {
	result, err := c.run(ctx, "/opt/sbin/ip", "route", "show", "dev", "br0")
	if err != nil {
		c.logWarn("client-route", "br0", "Failed to detect LAN subnet: "+err.Error())
		return ""
	}
	return parseLANSubnet(result.Stdout)
}

// parseLANSubnet extracts the first CIDR subnet from ip route output.
func parseLANSubnet(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		// Look for CIDR notation (e.g., "192.168.1.0/24")
		if strings.Contains(fields[0], "/") {
			return fields[0]
		}
	}
	return ""
}

// lookupRe matches "lookup NNN" in ip rule list output.
var lookupRe = regexp.MustCompile(`lookup\s+(\d+)`)

// parseRoutingTables extracts unique table numbers from ip rule list output.
func parseRoutingTables(output string) []int {
	seen := make(map[int]struct{})
	var tables []int

	for _, match := range lookupRe.FindAllStringSubmatch(output, -1) {
		num, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if _, ok := seen[num]; !ok {
			seen[num] = struct{}{}
			tables = append(tables, num)
		}
	}

	sort.Ints(tables)
	return tables
}
