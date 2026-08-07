package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/device"
)

const directConnIdle = 90 * time.Second

// directConn adapts WRAP PacketConn (one remote) to net.Conn for handleConn relay.
type directConn struct {
	pc     net.PacketConn
	remote net.Addr
}

func (c *directConn) Read(p []byte) (int, error) {
	n, addr, err := c.pc.ReadFrom(p)
	if err != nil {
		return 0, err
	}
	if addr != nil && c.remote != nil && addr.String() != c.remote.String() {
		return 0, fmt.Errorf("direct: unexpected remote %s", addr)
	}
	return n, nil
}

func (c *directConn) Write(p []byte) (int, error) {
	return c.pc.WriteTo(p, c.remote)
}

func (c *directConn) Close() error                       { return c.pc.Close() }
func (c *directConn) LocalAddr() net.Addr                { return c.pc.LocalAddr() }
func (c *directConn) RemoteAddr() net.Addr               { return c.remote }
func (c *directConn) SetDeadline(t time.Time) error      { return c.pc.SetDeadline(t) }
func (c *directConn) SetReadDeadline(t time.Time) error  { return c.pc.SetReadDeadline(t) }
func (c *directConn) SetWriteDeadline(t time.Time) error { return c.pc.SetWriteDeadline(t) }

func runDirectServer(ctx context.Context, listenAddr, wgEndpoint string, wgDev *device.Device, keys *wgKeys) error {
	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return fmt.Errorf("адрес %s: %w", listenAddr, err)
	}
	if serverWrapKeys.Count() == 0 {
		return fmt.Errorf("нет активных паролей для WRAP")
	}
	pl, err := listenWrapped(addr, serverWrapKeys)
	if err != nil {
		return fmt.Errorf("wrap: %w", err)
	}
	context.AfterFunc(ctx, func() { _ = pl.Close() })
	log.Printf("[DIRECT] WRAP UDP (без DTLS): %s", listenAddr)

	var acceptWg sync.WaitGroup
	for {
		pc, remote, err := pl.Accept()
		if err != nil {
			if ctx.Err() != nil {
				acceptWg.Wait()
				return nil
			}
			continue
		}
		acceptWg.Add(1)
		go func(c net.PacketConn, raddr net.Addr) {
			defer acceptWg.Done()
			defer c.Close()
			handleDirectConn(ctx, &directConn{pc: c, remote: raddr}, wgEndpoint, wgDev, keys)
		}(pc, remote)
	}
}

// handleDirectConn — GETCONF/WG relay без DTLS (official qWDTT -listen-direct).
func handleDirectConn(ctx context.Context, clientConn net.Conn, wgEndpoint string, wgDev *device.Device, keys *wgKeys) {
	atomic.AddInt64(&totalConns, 1)
	atomic.AddInt32(&activeConns, 1)
	defer atomic.AddInt32(&activeConns, -1)

	var connDeviceID string

	buf := make([]byte, 1600)
	clientConn.SetReadDeadline(time.Now().Add(directConnIdle))
	n, err := clientConn.Read(buf)
	if err != nil {
		return
	}
	clientConn.SetReadDeadline(time.Time{})

	firstPacket := buf[:n]
	firstStr := string(firstPacket)

	if stringsHasPrefix(firstStr, "GETCONF:") {
		connDeviceID, firstPacket, firstStr = handleDirectGetConf(clientConn, firstStr, buf, keys)
		if connDeviceID == "" && firstStr == "" {
			return
		}
	} else if stringsHasPrefix(firstStr, "AUTH:") {
		parts := splitPipe(stringsTrimSpace(stringsTrimPrefix(firstStr, "AUTH:")))
		if len(parts) > 0 {
			connDeviceID = parts[0]
		}
		clientConn.SetReadDeadline(time.Now().Add(directConnIdle))
		n, err = clientConn.Read(buf)
		if err != nil {
			return
		}
		clientConn.SetReadDeadline(time.Time{})
		firstPacket = buf[:n]
		firstStr = string(firstPacket)
	}

	if firstStr == "READY" {
		clientConn.Write([]byte("READY_OK"))
		clientConn.SetReadDeadline(time.Now().Add(directConnIdle))
		n, err = clientConn.Read(buf)
		if err != nil {
			return
		}
		clientConn.SetReadDeadline(time.Time{})
		firstPacket = buf[:n]
	}

	wgConn, err := net.Dial("udp", wgEndpoint)
	if err != nil {
		return
	}
	defer wgConn.Close()

	if uc, ok := wgConn.(*net.UDPConn); ok {
		uc.SetReadBuffer(2 * 1024 * 1024)
		uc.SetWriteBuffer(2 * 1024 * 1024)
	}

	if _, err := wgConn.Write(firstPacket); err != nil {
		return
	}
	atomic.AddInt64(&totalBytesFromClient, int64(len(firstPacket)))

	if connDeviceID != "" {
		activeDevicesMu.Lock()
		activeDevices[connDeviceID]++
		activeDevicesMu.Unlock()
		defer func() {
			activeDevicesMu.Lock()
			activeDevices[connDeviceID]--
			if activeDevices[connDeviceID] <= 0 {
				delete(activeDevices, connDeviceID)
			}
			activeDevicesMu.Unlock()
		}()
	}

	pctx, pcancel := context.WithCancel(ctx)
	defer pcancel()

	context.AfterFunc(pctx, func() {
		clientConn.SetDeadline(time.Now())
		wgConn.SetDeadline(time.Now())
	})

	var proxyWg sync.WaitGroup
	proxyWg.Add(2)

	go func() {
		defer proxyWg.Done()
		defer pcancel()
		b := getBuf()
		defer putBuf(b)
		for {
			select {
			case <-pctx.Done():
				return
			default:
			}
			clientConn.SetReadDeadline(time.Now().Add(directConnIdle))
			nn, err := clientConn.Read(*b)
			if err != nil {
				return
			}
			if nn == 1 && (*b)[0] == 0xFF {
				continue
			}
			atomic.AddInt64(&totalBytesFromClient, int64(nn))
			if _, err := wgConn.Write((*b)[:nn]); err != nil {
				return
			}
			wgConn.SetReadDeadline(time.Now().Add(directConnIdle))
		}
	}()

	go func() {
		defer proxyWg.Done()
		defer pcancel()
		b := getBuf()
		defer putBuf(b)
		for {
			select {
			case <-pctx.Done():
				return
			default:
			}
			wgConn.SetReadDeadline(time.Now().Add(directConnIdle))
			nn, err := wgConn.Read(*b)
			if err != nil {
				if isNetTimeout(err) {
					if pctx.Err() != nil {
						return
					}
					continue
				}
				return
			}
			atomic.AddInt64(&totalBytesToClient, int64(nn))
			if _, err := clientConn.Write((*b)[:nn]); err != nil {
				return
			}
			clientConn.SetReadDeadline(time.Now().Add(directConnIdle))
		}
	}()

	proxyWg.Wait()
}

func handleDirectGetConf(clientConn net.Conn, firstStr string, buf []byte, keys *wgKeys) (deviceID string, firstPacket []byte, nextStr string) {
	parts := splitPipe(stringsTrimSpace(stringsTrimPrefix(firstStr, "GETCONF:")))
	clientPort := "9000"
	devID := "unknown"
	password := ""
	if len(parts) > 0 {
		clientPort = parts[0]
	}
	if len(parts) > 1 {
		devID = parts[1]
	}
	if len(parts) > 2 {
		password = parts[2]
	}

	dbMutex.Lock()
	isMainPass := password != "" && password == db.MainPassword
	entry, isGenPass := db.Passwords[password]
	valid := isMainPass || (isGenPass && !isPasswordExpired(entry))

	if valid && isGenPass && entry.IsDeactivated {
		clientConn.Write([]byte("DENIED:deactivated"))
		log.Printf("[WG] Отказ: пароль %s деактивирован, запрос от %s", maskPassword(password), devID)
		dbMutex.Unlock()
		return "", nil, ""
	}
	if valid && isGenPass && !entry.canConnectAndBind(devID) {
		clientConn.Write([]byte("DENIED:device_mismatch"))
		log.Printf("[WG] Отказ: пароль %s достиг лимита устройств (%d), запрос от %s", maskPassword(password), entry.MaxDevices, devID)
		dbMutex.Unlock()
		return "", nil, ""
	}
	if valid {
		deviceID = devID
		if isGenPass {
			saveDB()
		}
		dev, exists := db.Devices[devID]
		if !exists {
			dev = &ClientDevice{DeviceID: devID, IP: getNextIP()}
			privB64, pubB64, keyErr := generateKeyPair()
			if keyErr == nil && dev.IP != "" {
				dev.PrivKey = privB64
				dev.PubKey = pubB64
				db.Devices[devID] = dev
				saveDB()
				log.Printf("[WG] Новое устройство %s (IP: %s)", devID, dev.IP)
			} else {
				dev = nil
			}
		}
		if dev != nil {
			upsertPeerInWG(globalWgDev, dev)
			clientConn.Write([]byte(buildClientConfig(keys.serverPublic, dev.PrivKey, dev.IP, clientPort)))
		} else {
			clientConn.Write([]byte("NOCONF"))
		}
		dbMutex.Unlock()
	} else {
		if isGenPass && isPasswordExpired(entry) {
			clientConn.Write([]byte("DENIED:expired"))
			log.Printf("[WG] Отказ: пароль %s истёк, от %s", maskPassword(password), devID)
		} else {
			clientConn.Write([]byte("DENIED:wrong_password"))
			log.Printf("[WG] Отказ (неверный пароль) от %s", devID)
		}
		dbMutex.Unlock()
		return "", nil, ""
	}

	clientConn.SetReadDeadline(time.Now().Add(directConnIdle))
	n, err := clientConn.Read(buf)
	if err != nil {
		return "", nil, ""
	}
	clientConn.SetReadDeadline(time.Time{})
	fp := buf[:n]
	return deviceID, fp, string(fp)
}

// tiny helpers to avoid importing strings in duplicate — server.go already imports strings.
func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
func stringsTrimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}
func stringsTrimPrefix(s, prefix string) string {
	if stringsHasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}
func splitPipe(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '|' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
