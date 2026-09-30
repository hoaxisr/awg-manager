//go:build linux

package traffic

import (
	"context"
	"encoding/binary"
	"net"
	"syscall"
	"time"
)

func htons(i uint16) uint16 {
	return (i<<8)&0xff00 | (i>>8)&0x00ff
}

// startLiveSniffer starts background raw packet capture to intercept DNS responses and TLS ClientHello SNI.
func (s *Service) startLiveSniffer(ctx context.Context, gen uint64) {
	s.snifferWg.Add(1)
	go func() {
		defer s.snifferWg.Done()
		opener := s.openPacketSocket
		if opener == nil {
			opener = func() (int, error) {
				return syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_IP)))
			}
		}
		// Open raw socket
		fd, err := opener()
		if err != nil {
			s.snifferMu.Lock()
			if s.snifferGen == gen {
				s.snifferActive = false
				if s.snifferCancel != nil {
					s.snifferCancel()
					s.snifferCancel = nil
				}
			}
			s.snifferMu.Unlock()
			s.log.Debug("raw packet socket open failed", "", err.Error())
			return
		}

		closer := s.closePacketSocket
		if closer == nil {
			closer = syscall.Close
		}
		defer func() {
			_ = closer(fd)
			s.snifferMu.Lock()
			if s.snifferGen == gen {
				s.snifferActive = false
				s.snifferCancel = nil
			}
			s.snifferMu.Unlock()
		}()

		tv := syscall.Timeval{Sec: 1, Usec: 0}
		_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

		recver := s.recvPacket
		if recver == nil {
			recver = func(sockFd int, buffer []byte) (int, error) {
				n, _, recvErr := syscall.Recvfrom(sockFd, buffer, 0)
				return n, recvErr
			}
		}

		buf := make([]byte, 2048)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, err := recver(fd, buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK || err == syscall.EINTR {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}

			if n < 14+20 { // Ethernet header (14) + IP header (min 20)
				continue
			}

			// Parse Ethernet (skip 14 bytes)
			ethProto := binary.BigEndian.Uint16(buf[12:14])
			if ethProto != 0x0800 { // IPv4
				continue
			}

			ipPacket := buf[14:n]
			ipHeaderLen := int(ipPacket[0]&0x0F) * 4
			if len(ipPacket) < ipHeaderLen {
				continue
			}

			protocol := ipPacket[9]
			dstIP := net.IP(ipPacket[16:20]).String()
			payload := ipPacket[ipHeaderLen:]

			if protocol == 17 { // UDP
				if len(payload) < 8 {
					continue
				}
				srcPort := binary.BigEndian.Uint16(payload[0:2])
				udpData := payload[8:]

				// DNS response from port 53
				if srcPort == 53 && len(udpData) >= 12 {
					ans := ParseDNSAnswers(udpData)
					if len(ans) > 0 {
						s.mu.Lock()
						for ip, dom := range ans {
							s.dnsCache[ip] = dom
						}
						s.mu.Unlock()
					}
				}
			} else if protocol == 6 { // TCP
				if len(payload) < 20 {
					continue
				}
				dstPort := binary.BigEndian.Uint16(payload[2:4])
				dataOffset := int(payload[12]>>4) * 4
				if len(payload) <= dataOffset {
					continue
				}
				tcpData := payload[dataOffset:]

				// TLS ClientHello to port 443
				if dstPort == 443 && len(tcpData) > 5 && tcpData[0] == 0x16 {
					sni := ExtractSNI(tcpData)
					if sni != "" {
						s.mu.Lock()
						s.dnsCache[dstIP] = sni
						s.mu.Unlock()
					}
				}
			}
		}
	}()
}
