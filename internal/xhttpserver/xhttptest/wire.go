// Package xhttptest provides test-only client wire encoders/decoders and C binary build
// fixtures for integration testing the mandatory C daemons (awgm-xhttpd and awgm-fpc).
// It contains zero server/relay/runtime logic and is never imported by production code.
package xhttptest

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
)

const (
	VLESSCmdTCP  = 0x01
	VLESSCmdUDP  = 0x02
	VLESSCmdMux  = 0x03
	VLESSAddrV4  = 0x01
	VLESSAddrDom = 0x02
	VLESSAddrV6  = 0x03

	MuxStatusNew   = 0x01
	MuxStatusKeep  = 0x02
	MuxStatusEnd   = 0x03
	MuxOptionData  = 0x01
	MuxOptionError = 0x02

	FPProtoMagic         = "FP01"
	FPProtoVersion       = "fp/1"
	FPHeaderProtoVersion = "X-FP-Version"
	FPHeaderAuth         = "X-FP-Auth"
	FPReqHeaderSize      = 54
	FPRespHeaderSize     = 58
	FPMaxRespPayload     = 512 * 1024

	FPOpOpen  = 1
	FPOpUp    = 2
	FPOpDown  = 3
	FPOpClose = 4

	FPStatusOK             = 0
	FPStatusBadRequest     = 1
	FPStatusUnauthorized   = 2
	FPStatusForbidden      = 3
	FPStatusUnknownSession = 4
	FPStatusResourceLimit  = 5
	FPStatusBackpressure   = 6
	FPStatusDialError      = 7

	Stage2ProbeClientID = "stage2probe"
)

type FPSessionID [16]byte

func (id FPSessionID) IsZero() bool {
	var z FPSessionID
	return id == z
}

func (id FPSessionID) String() string {
	return hex.EncodeToString(id[:])
}

type FPRequestEnvelope struct {
	Op             uint8
	Flags          uint8
	SessionID      FPSessionID
	UpOffset       uint64
	AckDownOffset  uint64
	WantDownOffset uint64
	MaxDownBytes   uint32
	Payload        []byte
}

type FPResponseEnvelope struct {
	Status          uint8
	Flags           uint8
	SessionID       FPSessionID
	AckedUpOffset   uint64
	DownOffsetStart uint64
	DownOffsetEnd   uint64
	TargetEOFOffset uint64
	Payload         []byte
}

func BuildVLESSReq(t testing.TB, uuidStr string, ip4 net.IP, port uint16, payload []byte) []byte {
	t.Helper()
	u, err := xhttpserver.ParseUUID(uuidStr)
	if err != nil {
		t.Fatalf("ParseUUID: %v", err)
	}
	var buf bytes.Buffer
	buf.WriteByte(0x00)
	buf.Write(u[:])
	buf.WriteByte(0x00)
	buf.WriteByte(VLESSCmdTCP)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], port)
	buf.Write(p[:])
	buf.WriteByte(VLESSAddrV4)
	buf.Write(ip4.To4())
	buf.Write(payload)
	return buf.Bytes()
}

func EncodeMuxFrame(subID uint16, status byte, opt byte, isUDP bool, host string, port uint16, payload []byte) []byte {
	var meta bytes.Buffer
	var idb [2]byte
	binary.BigEndian.PutUint16(idb[:], subID)
	meta.Write(idb[:])
	meta.WriteByte(status)
	if len(payload) > 0 {
		opt |= MuxOptionData
	}
	meta.WriteByte(opt)
	if status == MuxStatusNew {
		if isUDP {
			meta.WriteByte(0x02)
		} else {
			meta.WriteByte(0x01)
		}
		var pb [2]byte
		binary.BigEndian.PutUint16(pb[:], port)
		meta.Write(pb[:])
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			meta.WriteByte(VLESSAddrV4)
			meta.Write(ip.To4())
		} else {
			meta.WriteByte(VLESSAddrDom)
			meta.WriteByte(byte(len(host)))
			meta.WriteString(host)
		}
	}
	var frame bytes.Buffer
	var ml [2]byte
	binary.BigEndian.PutUint16(ml[:], uint16(meta.Len()))
	frame.Write(ml[:])
	frame.Write(meta.Bytes())
	if (opt & MuxOptionData) != 0 {
		var dl [2]byte
		binary.BigEndian.PutUint16(dl[:], uint16(len(payload)))
		frame.Write(dl[:])
		frame.Write(payload)
	}
	return frame.Bytes()
}

func EncodeFPRequest(f *FPRequestEnvelope) ([]byte, error) {
	if f == nil {
		return nil, errors.New("nil envelope")
	}
	out := make([]byte, FPReqHeaderSize+len(f.Payload))
	copy(out[0:4], FPProtoMagic)
	out[4] = f.Op
	out[5] = f.Flags
	copy(out[6:22], f.SessionID[:])
	binary.BigEndian.PutUint64(out[22:30], f.UpOffset)
	binary.BigEndian.PutUint64(out[30:38], f.AckDownOffset)
	binary.BigEndian.PutUint64(out[38:46], f.WantDownOffset)
	binary.BigEndian.PutUint32(out[46:50], f.MaxDownBytes)
	binary.BigEndian.PutUint32(out[50:54], uint32(len(f.Payload)))
	copy(out[54:], f.Payload)
	return out, nil
}

func DecodeFPResponse(raw []byte, maxPayload int) (*FPResponseEnvelope, error) {
	if len(raw) < FPRespHeaderSize {
		return nil, fmt.Errorf("short fp response: %d", len(raw))
	}
	if string(raw[0:4]) != FPProtoMagic {
		return nil, fmt.Errorf("bad fp magic: %q", string(raw[0:4]))
	}
	plen := int(binary.BigEndian.Uint32(raw[54:58]))
	if maxPayload > 0 && plen > maxPayload {
		return nil, fmt.Errorf("fp response payload %d exceeds %d", plen, maxPayload)
	}
	if len(raw) != FPRespHeaderSize+plen {
		return nil, fmt.Errorf("fp response length mismatch: got %d want %d", len(raw), FPRespHeaderSize+plen)
	}
	var env FPResponseEnvelope
	env.Status = raw[4]
	env.Flags = raw[5]
	copy(env.SessionID[:], raw[6:22])
	env.AckedUpOffset = binary.BigEndian.Uint64(raw[22:30])
	env.DownOffsetStart = binary.BigEndian.Uint64(raw[30:38])
	env.DownOffsetEnd = binary.BigEndian.Uint64(raw[38:46])
	env.TargetEOFOffset = binary.BigEndian.Uint64(raw[46:54])
	if plen > 0 {
		env.Payload = append([]byte(nil), raw[58:]...)
	}
	return &env, nil
}

func ComputeFPHMAC(secret []byte, method, path, clientID, tsStr, nonceHex string, body []byte) []byte {
	bodySum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, secret)
	for _, s := range []string{FPProtoVersion, method, path, clientID, tsStr, nonceHex} {
		mac.Write([]byte(s))
		mac.Write([]byte{0})
	}
	mac.Write(bodySum[:])
	return mac.Sum(nil)
}

func SignFPRequest(clientID string, secret []byte, method, path string, body []byte, now time.Time) (string, error) {
	id := strings.TrimSpace(clientID)
	if id == "" || len(secret) != xhttpserver.FPKeySize {
		return "", xhttpserver.ErrFPInvalidCredentials
	}
	if u, err := xhttpserver.ParseUUID(id); err == nil {
		if xhttpserver.IsUUIDDerivedFPSecret(secret, u) {
			return "", xhttpserver.ErrFPUUIDDerivedKey
		}
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	nonceHex := hex.EncodeToString(nonce[:])
	tsStr := strconv.FormatInt(now.UnixMilli(), 10)
	sig := ComputeFPHMAC(secret, method, path, id, tsStr, nonceHex, body)
	return fmt.Sprintf("%s:%s:%s:%s", id, tsStr, nonceHex, hex.EncodeToString(sig)), nil
}

var (
	buildXHTTPDOnce sync.Once
	builtXHTTPDPath string
	builtXHTTPDErr  error

	buildFPCOnce sync.Once
	builtFPCPath string
	builtFPCErr  error
)

func repoRootDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// RequireCXHTTPDBinary returns a real compiled awgm-xhttpd binary path (building c-core/xhttpd
// once if needed). On Linux/POSIX or whenever AWGM_REQUIRE_C_TESTS=1 is set, any missing compiler
// or C compilation failure fails closed via t.Fatalf. Skipping is permitted strictly on Windows
// hosts when AWGM_REQUIRE_C_TESTS!=1 as an explicit host OS environment limitation.
func RequireCXHTTPDBinary(t testing.TB) string {
	t.Helper()
	requireC := os.Getenv("AWGM_REQUIRE_C_TESTS") == "1"
	if env := strings.TrimSpace(os.Getenv("AWGM_C_XHTTPD_BIN")); env != "" {
		info, err := xhttpserver.InspectBinary(env)
		if err != nil || !info.Available {
			t.Fatalf("AWGM_C_XHTTPD_BIN=%q is invalid: err=%v reason=%s", env, err, info.Error)
		}
		return env
	}
	if runtime.GOOS == "windows" {
		if requireC {
			t.Fatalf("AWGM_REQUIRE_C_TESTS=1: POSIX C awgm-xhttpd daemon cannot run on Windows host")
		}
		t.Skip("skipping live POSIX C awgm-xhttpd integration test: Windows host environment limitation (POSIX sockets/signals required)")
	}
	buildXHTTPDOnce.Do(func() {
		cc, err := exec.LookPath("cc")
		if err != nil {
			cc, err = exec.LookPath("gcc")
		}
		if err != nil {
			builtXHTTPDErr = fmt.Errorf("no C compiler found in PATH: %w", err)
			return
		}
		outDir, err := os.MkdirTemp("", "awgm-c-xhttpd-test-*")
		if err != nil {
			builtXHTTPDErr = err
			return
		}
		outBin := filepath.Join(outDir, "awgm-xhttpd")
		srcDir := filepath.Join(repoRootDir(), "c-core", "xhttpd")
		cmd := exec.Command(cc, "-O2", "-Wall", "-Wextra", "-std=gnu11", "-pthread",
			"-o", outBin,
			filepath.Join(srcDir, "main.c"),
			filepath.Join(srcDir, "crypto.c"),
			filepath.Join(srcDir, "sniff_and_dial.c"),
			filepath.Join(srcDir, "finiteproto.c"),
			filepath.Join(srcDir, "vless_xhttp.c"),
			"-lpthread",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			builtXHTTPDErr = fmt.Errorf("compile awgm-xhttpd failed: %v\n%s", err, string(out))
			return
		}
		builtXHTTPDPath = outBin
	})
	if builtXHTTPDErr != nil {
		t.Fatalf("real C awgm-xhttpd binary unavailable on %s (AWGM_REQUIRE_C_TESTS=%q): %v", runtime.GOOS, os.Getenv("AWGM_REQUIRE_C_TESTS"), builtXHTTPDErr)
	}
	return builtXHTTPDPath
}

// RequireCFPCBinary returns a real compiled awgm-fpc binary path (building c-core/fpc once if needed).
// On Linux/POSIX or whenever AWGM_REQUIRE_C_TESTS=1 is set, any missing compiler or C compilation
// failure fails closed via t.Fatalf. Skipping is permitted strictly on Windows hosts when
// AWGM_REQUIRE_C_TESTS!=1 as an explicit host OS environment limitation.
func RequireCFPCBinary(t testing.TB) string {
	t.Helper()
	requireC := os.Getenv("AWGM_REQUIRE_C_TESTS") == "1"
	if env := strings.TrimSpace(os.Getenv("AWGM_C_FPC_BIN")); env != "" {
		st, err := os.Stat(env)
		if err != nil || !st.Mode().IsRegular() {
			t.Fatalf("AWGM_C_FPC_BIN=%q is invalid: %v", env, err)
		}
		return env
	}
	if runtime.GOOS == "windows" {
		if requireC {
			t.Fatalf("AWGM_REQUIRE_C_TESTS=1: POSIX C awgm-fpc daemon cannot run on Windows host")
		}
		t.Skip("skipping live POSIX C awgm-fpc integration test: Windows host environment limitation (POSIX sockets/signals required)")
	}
	buildFPCOnce.Do(func() {
		cc, err := exec.LookPath("cc")
		if err != nil {
			cc, err = exec.LookPath("gcc")
		}
		if err != nil {
			builtFPCErr = fmt.Errorf("no C compiler found in PATH: %w", err)
			return
		}
		outDir, err := os.MkdirTemp("", "awgm-c-fpc-test-*")
		if err != nil {
			builtFPCErr = err
			return
		}
		outBin := filepath.Join(outDir, "awgm-fpc")
		srcDir := filepath.Join(repoRootDir(), "c-core", "fpc")
		xhttpdDir := filepath.Join(repoRootDir(), "c-core", "xhttpd")
		cmd := exec.Command(cc, "-O2", "-Wall", "-Wextra", "-std=gnu11", "-pthread",
			"-o", outBin,
			filepath.Join(srcDir, "fpc.c"),
			filepath.Join(xhttpdDir, "crypto.c"),
			"-lpthread",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			builtFPCErr = fmt.Errorf("compile awgm-fpc failed: %v\n%s", err, string(out))
			return
		}
		builtFPCPath = outBin
	})
	if builtFPCErr != nil {
		t.Fatalf("real C awgm-fpc binary unavailable on %s (AWGM_REQUIRE_C_TESTS=%q): %v", runtime.GOOS, os.Getenv("AWGM_REQUIRE_C_TESTS"), builtFPCErr)
	}
	return builtFPCPath
}
