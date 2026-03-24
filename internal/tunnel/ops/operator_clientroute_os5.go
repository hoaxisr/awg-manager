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

// SetupClientRouteTable sets up a routing table with a default route through the tunnel
// and a LAN bypass route so local traffic is not affected.
func (o *OperatorOS5Impl) SetupClientRouteTable(ctx context.Context, kernelIface string, tableNum int) error {
	tableStr := strconv.Itoa(tableNum)

	// Add default route through tunnel
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "route", "replace", "default", "dev", kernelIface, "table", tableStr)
	if err != nil {
		return fmt.Errorf("setup route table %d: default route: %w", tableNum, exec.FormatError(result, err))
	}

	// Detect LAN subnet and add bypass route
	lanSubnet := o.detectLANSubnet(ctx)
	if lanSubnet != "" {
		result, err := o.ipRun(ctx, "/opt/sbin/ip", "route", "replace", lanSubnet, "dev", "br0", "table", tableStr)
		if err != nil {
			o.logWarn("client-route", kernelIface, fmt.Sprintf("Failed to add LAN route %s to table %d: %s", lanSubnet, tableNum, exec.FormatError(result, err)))
		}
	}

	return nil
}

// AddClientRule adds an ip rule to route traffic from a client IP through the given table.
// Removes any existing rule first for idempotency.
func (o *OperatorOS5Impl) AddClientRule(ctx context.Context, clientIP string, tableNum int) error {
	tableStr := strconv.Itoa(tableNum)

	// Remove existing rule (idempotent) — ignore error
	o.ipRun(ctx, "/opt/sbin/ip", "rule", "del", "from", clientIP, "lookup", tableStr)

	// Add new rule
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "rule", "add", "from", clientIP, "lookup", tableStr, "priority", tableStr)
	if err != nil {
		return fmt.Errorf("add client rule from %s lookup %d: %w", clientIP, tableNum, exec.FormatError(result, err))
	}

	return nil
}

// RemoveClientRule removes the ip rule for a client IP.
func (o *OperatorOS5Impl) RemoveClientRule(ctx context.Context, clientIP string, tableNum int) error {
	tableStr := strconv.Itoa(tableNum)

	result, err := o.ipRun(ctx, "/opt/sbin/ip", "rule", "del", "from", clientIP, "lookup", tableStr)
	if err != nil {
		return fmt.Errorf("remove client rule from %s lookup %d: %w", clientIP, tableNum, exec.FormatError(result, err))
	}

	return nil
}

// CleanupClientRouteTable flushes all routes in the table and removes all ip rules referencing it.
func (o *OperatorOS5Impl) CleanupClientRouteTable(ctx context.Context, tableNum int) error {
	tableStr := strconv.Itoa(tableNum)

	// Flush all routes in the table
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "route", "flush", "table", tableStr)
	if err != nil {
		o.logWarn("client-route", tableStr, fmt.Sprintf("Failed to flush table: %s", exec.FormatError(result, err)))
	}

	// Remove all rules referencing this table (loop until no more)
	for i := 0; i < 100; i++ {
		_, err := o.ipRun(ctx, "/opt/sbin/ip", "rule", "del", "lookup", tableStr)
		if err != nil {
			break // No more rules
		}
	}

	return nil
}

// ListUsedRoutingTables parses `ip rule list` output and returns unique table numbers.
func (o *OperatorOS5Impl) ListUsedRoutingTables(ctx context.Context) ([]int, error) {
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "rule", "list")
	if err != nil {
		return nil, fmt.Errorf("ip rule list: %w", exec.FormatError(result, err))
	}

	return parseRoutingTables(result.Stdout), nil
}

// detectLANSubnet detects the LAN subnet from br0 interface routes.
// Returns empty string if detection fails.
func (o *OperatorOS5Impl) detectLANSubnet(ctx context.Context) string {
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "route", "show", "dev", "br0")
	if err != nil {
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
