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
		if conn, ok := parseConntrackLine(scanner.Text()); ok {
			result = append(result, conn)
		}
	}
	return result
}

// parseConntrackLine parses a single /proc/net/nf_conntrack line.
// Returns (_, false) for entries that should be skipped (loopback, IPv6).
//
// Zero-copy: walks the line once via splitField and takes field values
// as substrings of the input. Avoids the strings.Fields allocation that
// dominated profiles on active routers (10k+ lines per read). Returns
// rawConn by value so the local doesn't escape to the heap.
func parseConntrackLine(line string) (rawConn, bool) {
	// Field 0: "ipv4" / "ipv6".
	f0, rest := splitField(line)
	if f0 != "ipv4" {
		return rawConn{}, false
	}
	// Field 1: L3 proto number — skip.
	_, rest = splitField(rest)
	// Field 2: L4 proto name (tcp/udp/icmp/…).
	proto, rest := splitField(rest)
	if proto == "" {
		return rawConn{}, false
	}

	var conn rawConn
	conn.Protocol = proto

	// Key=value pairs + an optional bare TCP state word. Only the FIRST
	// occurrence of src/dst/sport/dport is taken (original direction);
	// the two packets/bytes pairs (original + reply) are summed.
	var packets1, packets2, bytes1, bytes2 int64
	var packetsSeen, bytesSeen int
	var srcSeen, dstSeen, sportSeen, dportSeen bool

	for {
		var f string
		f, rest = splitField(rest)
		if f == "" {
			break
		}
		idx := strings.IndexByte(f, '=')
		if idx < 0 {
			// Bare word. The only one we care about is the TCP state.
			// Other bare words (L4 proto number, timeout, flags like
			// [ASSURED]) fall through untouched.
			if proto == "tcp" && conn.State == "" {
				switch f {
				case "ESTABLISHED", "SYN_SENT", "SYN_RECV", "FIN_WAIT",
					"CLOSE_WAIT", "LAST_ACK", "TIME_WAIT", "CLOSE":
					conn.State = f
				}
			}
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

	if conn.Src == "127.0.0.1" && conn.Dst == "127.0.0.1" {
		return rawConn{}, false
	}
	return conn, true
}

// splitField returns the first whitespace-delimited token in s plus the
// remainder. Returned token is a substring of s (no copy). Treats runs
// of ' ' or '\t' as a single separator; empty or all-space input yields
// ("", "").
func splitField(s string) (token, rest string) {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	if start >= len(s) {
		return "", ""
	}
	end := start
	for end < len(s) && s[end] != ' ' && s[end] != '\t' {
		end++
	}
	return s[start:end], s[end:]
}
