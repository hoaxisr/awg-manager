//go:build linux

package wireguard

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Old iproute2 does not reliably print the selected table for route get. Ask
// the kernel directly rather than inventing table provenance from text.
func lookupMarkedRoute(ctx context.Context, destination netip.Addr, mark uint32) ([]byte, error) {
	if !destination.Is4() {
		return nil, errors.New("legacy marked lookup requires IPv4")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	request := make([]byte, 44)
	binary.NativeEndian.PutUint32(request[:4], 44)
	binary.NativeEndian.PutUint16(request[4:6], unix.RTM_GETROUTE)
	binary.NativeEndian.PutUint16(request[6:8], unix.NLM_F_REQUEST)
	binary.NativeEndian.PutUint32(request[8:12], 1)
	request[16] = unix.AF_INET
	request[17] = 32
	binary.NativeEndian.PutUint32(request[24:28], unix.RTM_F_LOOKUP_TABLE)
	binary.NativeEndian.PutUint16(request[28:30], 8)
	binary.NativeEndian.PutUint16(request[30:32], unix.RTA_DST)
	ip := destination.As4()
	copy(request[32:36], ip[:])
	binary.NativeEndian.PutUint16(request[36:38], 8)
	binary.NativeEndian.PutUint16(request[38:40], unix.RTA_MARK)
	binary.NativeEndian.PutUint32(request[40:44], mark)
	if err = unix.Sendto(fd, request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	buffer := make([]byte, 65536)
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, context.DeadlineExceeded
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err = unix.Poll(poll, 50); err != nil {
			if err == unix.EINTR {
				continue
			}
			return nil, err
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, from, err := unix.Recvfrom(fd, buffer, 0)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EINTR {
				continue
			}
			return nil, err
		}
		kernel, ok := from.(*unix.SockaddrNetlink)
		if !ok || kernel.Pid != 0 {
			return nil, errors.New("marked route response is not from kernel")
		}
		messages, err := syscall.ParseNetlinkMessage(buffer[:n])
		if err != nil {
			return nil, err
		}
		for _, message := range messages {
			if message.Header.Seq != 1 {
				continue
			}
			if message.Header.Type == unix.NLMSG_ERROR {
				if len(message.Data) < 4 {
					return nil, errors.New("truncated route error")
				}
				code := int32(binary.NativeEndian.Uint32(message.Data[:4]))
				if code != 0 {
					return nil, syscall.Errno(-code)
				}
				continue
			}
			if message.Header.Type != unix.RTM_NEWROUTE || len(message.Data) < 12 || message.Data[0] != unix.AF_INET {
				return nil, errors.New("unexpected marked route response")
			}
			attrs, err := syscall.ParseNetlinkRouteAttr(&message)
			if err != nil {
				return nil, err
			}
			row := map[string]any{"dst": destination.String(), "table": uint32(message.Data[4]), "protocol": uint32(message.Data[5])}
			for _, attr := range attrs {
				if len(attr.Value) < 4 {
					continue
				}
				switch attr.Attr.Type {
				case unix.RTA_TABLE:
					row["table"] = binary.NativeEndian.Uint32(attr.Value[:4])
				case unix.RTA_OIF:
					iface, e := net.InterfaceByIndex(int(binary.NativeEndian.Uint32(attr.Value[:4])))
					if e != nil {
						return nil, e
					}
					row["dev"] = iface.Name
				case unix.RTA_PREFSRC:
					var ip [4]byte
					copy(ip[:], attr.Value[:4])
					row["prefsrc"] = netip.AddrFrom4(ip).String()
				case unix.RTA_GATEWAY:
					var ip [4]byte
					copy(ip[:], attr.Value[:4])
					row["gateway"] = netip.AddrFrom4(ip).String()
				}
			}
			if row["dev"] == nil || row["prefsrc"] == nil || row["table"] == uint32(0) {
				return nil, errors.New("incomplete kernel marked route proof")
			}
			return json.Marshal([]any{row})
		}
	}
}
