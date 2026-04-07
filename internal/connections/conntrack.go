package connections

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// rawConn extends Connection with internal fields used during parsing.
type rawConn struct {
	Connection
	ifw int // output interface index (from Keenetic conntrack extension)
}

// conntrackPath is the default conntrack file. Overridable for testing.
var conntrackPath = "/proc/net/nf_conntrack"

// readConntrackFile reads and parses the system conntrack file.
func readConntrackFile() ([]rawConn, error) {
	f, err := os.Open(conntrackPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseConntrack(f), nil
}

// parseConntrack reads all lines and returns parsed connections.
// Skips loopback and IPv6 entries.
func parseConntrack(r io.Reader) []rawConn {
	var result []rawConn
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if conn := parseConntrackLine(scanner.Text()); conn != nil {
			result = append(result, *conn)
		}
	}
	return result
}

// parseConntrackLine parses a single /proc/net/nf_conntrack line.
// Returns nil for entries that should be skipped (loopback, IPv6).
func parseConntrackLine(line string) *rawConn {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return nil
	}

	// Skip IPv6
	if fields[0] != "ipv4" {
		return nil
	}

	proto := fields[2]

	var conn rawConn
	conn.Protocol = proto

	// Parse TCP state (appears as a bare word like ESTABLISHED, SYN_SENT, etc.)
	if proto == "tcp" {
		for _, f := range fields[5:] {
			switch f {
			case "ESTABLISHED", "SYN_SENT", "SYN_RECV", "FIN_WAIT",
				"CLOSE_WAIT", "LAST_ACK", "TIME_WAIT", "CLOSE":
				conn.State = f
			}
			if conn.State != "" {
				break
			}
		}
	}

	// Parse key=value pairs. Take first occurrence of src/dst/sport/dport (original direction).
	var srcSeen, dstSeen, sportSeen, dportSeen bool
	var packets1, packets2, bytes1, bytes2 int64
	var packetsSeen, bytesSeen int

	for _, f := range fields {
		idx := strings.IndexByte(f, '=')
		if idx < 0 {
			continue
		}
		key := f[:idx]
		val := f[idx+1:]

		switch key {
		case "src":
			if !srcSeen {
				conn.Src = val
				srcSeen = true
			}
		case "dst":
			if !dstSeen {
				conn.Dst = val
				dstSeen = true
			}
		case "sport":
			if !sportSeen {
				conn.SrcPort, _ = strconv.Atoi(val)
				sportSeen = true
			}
		case "dport":
			if !dportSeen {
				conn.DstPort, _ = strconv.Atoi(val)
				dportSeen = true
			}
		case "packets":
			if packetsSeen == 0 {
				packets1, _ = strconv.ParseInt(val, 10, 64)
			} else if packetsSeen == 1 {
				packets2, _ = strconv.ParseInt(val, 10, 64)
			}
			packetsSeen++
		case "bytes":
			if bytesSeen == 0 {
				bytes1, _ = strconv.ParseInt(val, 10, 64)
			} else if bytesSeen == 1 {
				bytes2, _ = strconv.ParseInt(val, 10, 64)
			}
			bytesSeen++
		case "ifw":
			conn.ifw, _ = strconv.Atoi(val)
		case "mac":
			conn.ClientMAC = val
		}
	}

	conn.Packets = packets1 + packets2
	conn.Bytes = bytes1 + bytes2

	// Skip loopback
	if conn.Src == "127.0.0.1" && conn.Dst == "127.0.0.1" {
		return nil
	}

	return &conn
}
