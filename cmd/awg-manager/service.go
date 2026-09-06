package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

const (
	// pidFile lives on the system tmpfs (cleared on every boot) so an
	// unclean reboot can never leave a stale PID pointing at whatever
	// process eventually inherits that PID slot on the next uptime.
	// /var/run is FHS-canonical and always tmpfs on Keenetic; /opt/var/run
	// is Entware-persistent storage and was the source of the stale-PID
	// startup-block bug.
	pidFile = "/var/run/awg-manager.pid"
	// legacyPidFile is the pre-move location; one-shot cleanup on startup
	// removes it after an upgrade so the old file does not linger.
	legacyPidFile = "/opt/var/run/awg-manager.pid"
	// serviceStderrLog captures the daemon's stderr under --service start:
	// panic traces and early-boot warnings that predate the app logger.
	// Lives on the /var/run tmpfs like the PID file: it is read while the
	// router is up (a dead daemon is diagnosed without a reboot), and it
	// must not cost a flash write on every start (#854).
	serviceStderrLog = "/var/run/awg-manager/stderr.log"
)

// runService handles --service flag: start/stop/restart/status.
// This replaces the shell logic that was previously in S99awg-manager.
func runService(action, dataDir string) {
	switch action {
	case "start":
		serviceStart(dataDir)
	case "stop":
		serviceStop()
	case "restart":
		serviceStop()
		time.Sleep(time.Second)
		serviceStart(dataDir)
	case "status":
		serviceStatus(dataDir)
	default:
		fmt.Fprintf(os.Stderr, "Unknown service action: %s\nUsage: --service {start|stop|restart|status}\n", action)
		os.Exit(1)
	}
}

// serviceStart starts the daemon as a background process with PID file management.
func serviceStart(dataDir string) {
	// Check if already running
	if pid, running := readPIDFile(); running {
		fmt.Printf("AWG Manager already running (PID %d)\n", pid)
		return
	}

	fmt.Println("Starting AWG Manager...")

	// Ensure directories. /var/run is system tmpfs and always exists,
	// but MkdirAll is idempotent so harmless to call.
	os.MkdirAll(filepath.Dir(serviceStderrLog), 0755)
	os.MkdirAll("/opt/var/log", 0755)
	os.MkdirAll(dataDir, 0755)

	// Resolve executable path
	executable, err := os.Executable()
	if err != nil {
		executable = os.Args[0]
	}

	// Ensure system binaries and libraries are available for child processes
	ensureServiceEnv()

	// Start the daemon without --service flag
	cmd := exec.Command(executable, "-data-dir", dataDir)
	setServiceSysProcAttr(cmd)

	// O_RDWR, not os.Open (O_RDONLY): writes to a read-only /dev/null fd fail
	// with EBADF, which used to silently eat every stderr warning and panic
	// trace of the daemon.
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		cmd.Stdin = devNull
		defer devNull.Close()
	}
	// Keep panic traces and early-boot warnings (bind fallbacks, corrupt
	// settings recovery) somewhere findable instead of /dev/null.
	if logf, lerr := os.OpenFile(serviceStderrLog, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); lerr == nil {
		cmd.Stderr = logf
		defer logf.Close()
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start AWG Manager: %v\n", err)
		os.Exit(1)
	}

	childPID := cmd.Process.Pid

	// Write PID file
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(childPID)+"\n"), 0644)

	// Detach from child — it becomes an orphan re-parented to init
	cmd.Process.Release()

	// Wait for process to start (up to 5 seconds)
	for i := 0; i < 5; i++ {
		time.Sleep(time.Second)
		if isProcessRunning(childPID) {
			host, port := getServiceEndpoint(dataDir)
			fmt.Printf("AWG Manager started: http://%s:%d\n", host, port)
			return
		}
	}

	fmt.Fprintln(os.Stderr, "AWG Manager failed to start")
	os.Remove(pidFile)
	os.Exit(1)
}

// serviceStop stops the running daemon via PID file.
func serviceStop() {
	pid, running := readPIDFile()
	if !running {
		fmt.Println("AWG Manager stopped")
		return
	}

	fmt.Println("Stopping AWG Manager...")

	process, err := os.FindProcess(pid)
	if err != nil {
		os.Remove(pidFile)
		fmt.Println("AWG Manager stopped")
		return
	}

	// Send SIGTERM for graceful shutdown
	_ = process.Signal(syscall.SIGTERM)

	// Wait up to 5 seconds for process to exit
	for i := 0; i < 5; i++ {
		time.Sleep(time.Second)
		if !isProcessRunning(pid) {
			break
		}
	}

	// Force kill if still running
	if isProcessRunning(pid) {
		_ = process.Signal(syscall.SIGKILL)
	}

	os.Remove(pidFile)
	fmt.Println("AWG Manager stopped")
}

// serviceStatus checks if the daemon is running and prints its endpoint.
func serviceStatus(dataDir string) {
	pid, running := readPIDFile()
	if !running {
		fmt.Println("AWG Manager not running")
		os.Exit(1)
	}

	host, port := getServiceEndpoint(dataDir)
	fmt.Printf("AWG Manager running (PID %d): http://%s:%d\n", pid, host, port)
}

// readPIDFile reads the PID file and checks if the process is alive.
// Returns the PID and whether the process is running.
func readPIDFile() (int, bool) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !isProcessRunning(pid) {
		// Stale PID file
		os.Remove(pidFile)
		return 0, false
	}
	return pid, true
}

// isProcessRunning checks if a process with the given PID is an awg-manager
// instance. /proc/<pid>/cmdline is the NUL-separated argv. We match on the
// basename of argv[0] rather than on the whole buffer so an argument that
// happens to contain "awg-manager" (e.g. "-data-dir /opt/etc/awg-manager")
// for an unrelated process that inherited the recycled PID does not
// produce a false positive.
func isProcessRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	argv0 := string(data)
	if i := strings.IndexByte(argv0, 0); i >= 0 {
		argv0 = argv0[:i]
	}
	return filepath.Base(argv0) == "awg-manager"
}

// getServiceEndpoint reads settings to determine the service host:port for display.
func getServiceEndpoint(dataDir string) (string, int) {
	port := 2222
	settingsFile := filepath.Join(dataDir, "settings.json")
	if data, err := os.ReadFile(settingsFile); err == nil {
		var s struct {
			Server struct {
				Port int `json:"port"`
			} `json:"server"`
		}
		if json.Unmarshal(data, &s) == nil && s.Server.Port > 0 {
			port = s.Server.Port
		}
	}

	// Use br0 (LAN bridge) for display — this is what the user connects from
	host := netif.FirstIPv4(storage.DefaultInterface)
	if host == "" {
		host = "192.168.1.1"
	}

	return host, port
}
