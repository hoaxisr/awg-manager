package procmon

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// CpuCore describes CPU load for a single core or total.
type CpuCore struct {
	ID     string  `json:"id"`     // "total", "cpu0", "cpu1", etc.
	User   float64 `json:"user"`   // %
	System float64 `json:"system"` // %
	Nice   float64 `json:"nice"`   // %
	Idle   float64 `json:"idle"`   // %
	IoWait float64 `json:"iowait"` // %
	Usage  float64 `json:"usage"`  // Total non-idle %
}

// MemoryInfo describes RAM and Swap usage in bytes.
type MemoryInfo struct {
	Total        uint64  `json:"total"`
	Free         uint64  `json:"free"`
	Available    uint64  `json:"available"`
	Used         uint64  `json:"used"`
	Buffers      uint64  `json:"buffers"`
	Cached       uint64  `json:"cached"`
	SwapTotal    uint64  `json:"swapTotal"`
	SwapFree     uint64  `json:"swapFree"`
	SwapUsed     uint64  `json:"swapUsed"`
	UsagePercent float64 `json:"usagePercent"`
}

// ProcessItem describes a single Linux process.
type ProcessItem struct {
	PID           int     `json:"pid"`
	PPID          int     `json:"ppid"`
	User          string  `json:"user"`
	Priority      int     `json:"priority"`
	Nice          int     `json:"nice"`
	Threads       int     `json:"threads"`
	State         string  `json:"state"` // "R", "S", "D", "Z", "T"
	CPUPercent    float64 `json:"cpuPercent"`
	MemoryRSS     uint64  `json:"memoryRss"`   // bytes
	MemoryVSize   uint64  `json:"memoryVsize"` // bytes
	MemoryPercent float64 `json:"memoryPercent"`
	Name          string  `json:"name"`
	Cmdline       string  `json:"cmdline"`
	Exe           string  `json:"exe,omitempty"`
	Service       string  `json:"service,omitempty"`
	IsSelf        bool    `json:"isSelf"`
	IsCritical    bool    `json:"isCritical"`
	IsKernel      bool    `json:"isKernel"`
}

// SystemSnapshot is the full payload returned to the frontend.
type SystemSnapshot struct {
	Timestamp       time.Time     `json:"timestamp"`
	UptimeSeconds   uint64        `json:"uptimeSeconds"`
	LoadAvg         [3]float64    `json:"loadAvg"` // 1m, 5m, 15m
	CPUModel        string        `json:"cpuModel"`
	CPUArchitecture string        `json:"cpuArchitecture"`
	CPUCount        int           `json:"cpuCount"`
	Cores           []CpuCore     `json:"cores"` // index 0 is total, followed by cpu0, cpu1...
	Memory          MemoryInfo    `json:"memory"`
	ProcessSummary  ProcSummary   `json:"processSummary"`
	Processes       []ProcessItem `json:"processes"`
}

// ProcSummary counts process states.
type ProcSummary struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Sleeping int `json:"sleeping"`
	Stopped  int `json:"stopped"`
	Zombie   int `json:"zombie"`
	Threads  int `json:"threads"`
}

type cpuSample struct {
	user    uint64
	nice    uint64
	system  uint64
	idle    uint64
	iowait  uint64
	irq     uint64
	softirq uint64
	steal   uint64
}

func (s cpuSample) total() uint64 {
	return s.user + s.nice + s.system + s.idle + s.iowait + s.irq + s.softirq + s.steal
}

func (s cpuSample) active() uint64 {
	return s.user + s.nice + s.system + s.irq + s.softirq + s.steal
}

// Sampler collects snapshots from /proc.
type Sampler struct {
	mu           sync.Mutex
	procDir      string
	lastSample   time.Time
	lastCPUs     map[string]cpuSample
	lastProcCPUs map[int]uint64 // PID -> (utime + stime)
	uidCache     map[int]string
}

// NewSampler creates a Sampler.
func NewSampler() *Sampler {
	return &Sampler{
		procDir:      "/proc",
		lastCPUs:     make(map[string]cpuSample),
		lastProcCPUs: make(map[int]uint64),
		uidCache:     make(map[int]string),
	}
}

// Snapshot reads and computes all system and process metrics.
func (s *Sampler) Snapshot() (*SystemSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	procDir := s.procDir
	if procDir == "" {
		procDir = "/proc"
	}

	snap := &SystemSnapshot{
		Timestamp: now,
		Cores:     make([]CpuCore, 0),
		Processes: make([]ProcessItem, 0),
	}

	// 0. Hardware / CPU info
	snap.CPUModel = readDeviceModel()
	snap.CPUArchitecture = readCPUArch(filepath.Join(procDir, "cpuinfo"))

	// 1. Uptime
	snap.UptimeSeconds = readUptime(filepath.Join(procDir, "uptime"))

	// 2. Load average
	snap.LoadAvg = readLoadAvg(filepath.Join(procDir, "loadavg"))

	// 3. Memory
	mem, err := readMemInfo(filepath.Join(procDir, "meminfo"))
	if err == nil {
		snap.Memory = mem
	}

	// 4. CPU Stat
	currentCPUs, err := readCPUStat(filepath.Join(procDir, "stat"))
	if err == nil {
		// Calculate CPU core percentages
		var totalDelta uint64 = 1
		for _, id := range sortedCPUIDs(currentCPUs) {
			cur := currentCPUs[id]
			prev, hasPrev := s.lastCPUs[id]
			core := CpuCore{ID: id}

			if hasPrev && cur.total() > prev.total() {
				deltaTotal := float64(cur.total() - prev.total())
				if id == "total" {
					totalDelta = cur.total() - prev.total()
				}
				core.User = clampPercent(float64(cur.user-prev.user) / deltaTotal * 100)
				core.System = clampPercent(float64(cur.system-prev.system) / deltaTotal * 100)
				core.Nice = clampPercent(float64(cur.nice-prev.nice) / deltaTotal * 100)
				core.Idle = clampPercent(float64(cur.idle-prev.idle) / deltaTotal * 100)
				core.IoWait = clampPercent(float64(cur.iowait-prev.iowait) / deltaTotal * 100)
				core.Usage = clampPercent(float64(cur.active()-prev.active()) / deltaTotal * 100)
			} else {
				// Initial / fallback
				t := float64(cur.total())
				if t > 0 {
					core.Usage = clampPercent(float64(cur.active()) / t * 100)
				}
			}
			snap.Cores = append(snap.Cores, core)
		}
		s.lastCPUs = currentCPUs

		// 5. Processes
		procs, summary, newProcCPUs := s.readProcesses(procDir, snap.Memory.Total, totalDelta)
		snap.Processes = procs
		snap.ProcessSummary = summary
		s.lastProcCPUs = newProcCPUs
	}

	s.lastSample = now
	return snap, nil
}

func (s *Sampler) readProcesses(procDir string, totalMem uint64, totalCpuDelta uint64) ([]ProcessItem, ProcSummary, map[int]uint64) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return []ProcessItem{}, ProcSummary{}, make(map[int]uint64)
	}

	newProcCPUs := make(map[int]uint64, len(entries))
	procs := make([]ProcessItem, 0, len(entries))
	summary := ProcSummary{}
	numCPUs := countOnlineCPUs(s.lastCPUs)
	selfPID := os.Getpid()

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}

		statPath := filepath.Join(procDir, e.Name(), "stat")
		pStat, err := parseProcStat(statPath)
		if err != nil {
			continue
		}

		summary.Total++
		summary.Threads += pStat.threads
		switch pStat.state {
		case "R":
			summary.Running++
		case "S", "D":
			summary.Sleeping++
		case "T":
			summary.Stopped++
		case "Z":
			summary.Zombie++
		default:
			summary.Sleeping++
		}

		item := ProcessItem{
			PID:         pid,
			PPID:        pStat.ppid,
			Priority:    pStat.priority,
			Nice:        pStat.nice,
			Threads:     pStat.threads,
			State:       pStat.state,
			Name:        pStat.comm,
			MemoryRSS:   pStat.rssBytes,
			MemoryVSize: pStat.vsizeBytes,
			IsSelf:      pid == selfPID,
		}

		if totalMem > 0 {
			item.MemoryPercent = clampPercent(float64(item.MemoryRSS) / float64(totalMem) * 100)
		}

		// Calculate process CPU %
		currentTicks := pStat.utime + pStat.stime
		newProcCPUs[pid] = currentTicks

		prevTicks, hasPrev := s.lastProcCPUs[pid]
		if hasPrev && currentTicks >= prevTicks && totalCpuDelta > 0 {
			deltaProcess := float64(currentTicks - prevTicks)
			cpuPct := (deltaProcess / float64(totalCpuDelta)) * 100 * float64(numCPUs)
			item.CPUPercent = clampPercent(cpuPct)
		}

		// Cmdline
		cmdBytes, _ := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline"))
		if len(cmdBytes) > 0 {
			item.Cmdline = strings.ReplaceAll(string(cmdBytes), "\x00", " ")
			item.Cmdline = strings.TrimSpace(item.Cmdline)
		}
		if item.Cmdline == "" {
			item.Cmdline = fmt.Sprintf("[%s]", pStat.comm)
		}

		// Kernel thread detection (ppid == 2 or kthreadd or empty cmdline)
		item.IsKernel = pStat.ppid == 2 || (pid == 2 && pStat.comm == "kthreadd") || len(cmdBytes) == 0

		// Exe link
		exeLink, _ := os.Readlink(filepath.Join(procDir, e.Name(), "exe"))
		item.Exe = exeLink

		// User
		item.User = s.getUserForPID(procDir, pid)

		// Critical check
		item.IsCritical = IsCriticalProcess(item.Name, item.Exe)

		// Service association
		if strings.HasPrefix(item.Name, "S") || strings.HasPrefix(item.Name, "K") || strings.Contains(item.Cmdline, "/opt/etc/init.d/") {
			item.Service = filepath.Base(item.Name)
		}

		procs = append(procs, item)
	}

	// Default sort by CPU % descending, then Memory RSS descending
	sort.Slice(procs, func(i, j int) bool {
		if procs[i].CPUPercent != procs[j].CPUPercent {
			return procs[i].CPUPercent > procs[j].CPUPercent
		}
		return procs[i].MemoryRSS > procs[j].MemoryRSS
	})

	return procs, summary, newProcCPUs
}

func (s *Sampler) getUserForPID(procDir string, pid int) string {
	statusBytes, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "status"))
	if err != nil {
		return "root"
	}
	uid := 0
	for _, line := range strings.Split(string(statusBytes), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				uid, _ = strconv.Atoi(fields[1])
			}
			break
		}
	}

	if name, ok := s.uidCache[uid]; ok {
		return name
	}

	name := "root"
	if uid == 0 {
		name = "root"
	} else if uid == 65534 {
		name = "nobody"
	} else {
		name = strconv.Itoa(uid)
	}
	s.uidCache[uid] = name
	return name
}

// KillProcess sends SIGTERM or SIGKILL to a PID.
func (s *Sampler) KillProcess(pid int, sigName string) error {
	if pid <= 1 {
		return fmt.Errorf("cannot kill system init process (PID %d)", pid)
	}

	exeLink, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	stat, _ := parseProcStat(fmt.Sprintf("/proc/%d/stat", pid))

	if IsCriticalProcess(stat.comm, exeLink) {
		return fmt.Errorf("cannot kill critical process %q", stat.comm)
	}

	if pid == os.Getpid() {
		return fmt.Errorf("cannot kill own process via API")
	}

	var sig os.Signal = syscall.SIGTERM
	if strings.EqualFold(sigName, "SIGKILL") || strings.EqualFold(sigName, "KILL") || sigName == "9" {
		sig = syscall.SIGKILL
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	return proc.Signal(sig)
}

type procStatParsed struct {
	comm       string
	state      string
	ppid       int
	utime      uint64
	stime      uint64
	priority   int
	nice       int
	threads    int
	vsizeBytes uint64
	rssBytes   uint64
}

func parseProcStat(statPath string) (procStatParsed, error) {
	var res procStatParsed
	data, err := os.ReadFile(statPath)
	if err != nil {
		return res, err
	}
	content := string(data)

	// Format: pid (comm) state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt cmajflt utime stime cutime cstime priority nice num_threads itrealvalue starttime vsize rss ...
	openParen := strings.Index(content, "(")
	closeParen := strings.LastIndex(content, ")")
	if openParen == -1 || closeParen == -1 || closeParen <= openParen {
		return res, fmt.Errorf("invalid stat format")
	}

	res.comm = content[openParen+1 : closeParen]
	remainder := strings.TrimSpace(content[closeParen+1:])
	fields := strings.Fields(remainder)
	if len(fields) < 22 {
		return res, fmt.Errorf("not enough fields in stat")
	}

	// fields[0] is state
	res.state = fields[0]
	res.ppid, _ = strconv.Atoi(fields[1])
	res.utime, _ = strconv.ParseUint(fields[11], 10, 64)
	res.stime, _ = strconv.ParseUint(fields[12], 10, 64)
	res.priority, _ = strconv.Atoi(fields[15])
	res.nice, _ = strconv.Atoi(fields[16])
	res.threads, _ = strconv.Atoi(fields[17])
	res.vsizeBytes, _ = strconv.ParseUint(fields[20], 10, 64)

	// RSS is in pages (multiply by pageSize = 4096 on Linux)
	pages, _ := strconv.ParseUint(fields[21], 10, 64)
	res.rssBytes = pages * 4096

	return res, nil
}

func readCPUStat(statPath string) (map[string]cpuSample, error) {
	f, err := os.Open(statPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	res := make(map[string]cpuSample)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		id := fields[0]
		if id == "cpu" {
			id = "total"
		}
		var s cpuSample
		s.user, _ = strconv.ParseUint(fields[1], 10, 64)
		s.nice, _ = strconv.ParseUint(fields[2], 10, 64)
		s.system, _ = strconv.ParseUint(fields[3], 10, 64)
		s.idle, _ = strconv.ParseUint(fields[4], 10, 64)
		if len(fields) > 5 {
			s.iowait, _ = strconv.ParseUint(fields[5], 10, 64)
		}
		if len(fields) > 6 {
			s.irq, _ = strconv.ParseUint(fields[6], 10, 64)
		}
		if len(fields) > 7 {
			s.softirq, _ = strconv.ParseUint(fields[7], 10, 64)
		}
		if len(fields) > 8 {
			s.steal, _ = strconv.ParseUint(fields[8], 10, 64)
		}
		res[id] = s
	}
	return res, scanner.Err()
}

func readMemInfo(memPath string) (MemoryInfo, error) {
	var m MemoryInfo
	f, err := os.Open(memPath)
	if err != nil {
		return m, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		valFields := strings.Fields(parts[1])
		if len(valFields) == 0 {
			continue
		}
		valKb, _ := strconv.ParseUint(valFields[0], 10, 64)
		valBytes := valKb * 1024

		switch k {
		case "MemTotal":
			m.Total = valBytes
		case "MemFree":
			m.Free = valBytes
		case "MemAvailable":
			m.Available = valBytes
		case "Buffers":
			m.Buffers = valBytes
		case "Cached":
			m.Cached = valBytes
		case "SwapTotal":
			m.SwapTotal = valBytes
		case "SwapFree":
			m.SwapFree = valBytes
		}
	}

	if m.Available == 0 {
		m.Available = m.Free + m.Buffers + m.Cached
	}
	if m.Total > m.Available {
		m.Used = m.Total - m.Available
	}
	if m.SwapTotal > m.SwapFree {
		m.SwapUsed = m.SwapTotal - m.SwapFree
	}
	if m.Total > 0 {
		m.UsagePercent = clampPercent(float64(m.Used) / float64(m.Total) * 100)
	}

	return m, scanner.Err()
}

func readLoadAvg(loadPath string) [3]float64 {
	var res [3]float64
	data, err := os.ReadFile(loadPath)
	if err != nil {
		return res
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		res[0], _ = strconv.ParseFloat(fields[0], 64)
		res[1], _ = strconv.ParseFloat(fields[1], 64)
		res[2], _ = strconv.ParseFloat(fields[2], 64)
	}
	return res
}

func readUptime(uptimePath string) uint64 {
	data, err := os.ReadFile(uptimePath)
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 1 {
		sec, _ := strconv.ParseFloat(fields[0], 64)
		return uint64(sec)
	}
	return 0
}

func sortedCPUIDs(cpus map[string]cpuSample) []string {
	var ids []string
	if _, ok := cpus["total"]; ok {
		ids = append(ids, "total")
	}
	var cores []string
	for k := range cpus {
		if k != "total" && strings.HasPrefix(k, "cpu") {
			cores = append(cores, k)
		}
	}
	sort.Slice(cores, func(i, j int) bool {
		idxI, _ := strconv.Atoi(strings.TrimPrefix(cores[i], "cpu"))
		idxJ, _ := strconv.Atoi(strings.TrimPrefix(cores[j], "cpu"))
		return idxI < idxJ
	})
	return append(ids, cores...)
}

func countOnlineCPUs(cpus map[string]cpuSample) int {
	c := 0
	for k := range cpus {
		if k != "total" && strings.HasPrefix(k, "cpu") {
			c++
		}
	}
	if c <= 0 {
		return 1
	}
	return c
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return float64(int(v*10)) / 10 // 1 decimal place
}

func readDeviceModel() string {
	data, err := os.ReadFile("/proc/device-tree/model")
	if err == nil && len(data) > 0 {
		clean := strings.Trim(string(data), "\x00 \r\n\t")
		if clean != "" {
			return clean
		}
	}
	return ""
}

func readCPUArch(cpuinfoPath string) string {
	data, err := os.ReadFile(cpuinfoPath)
	if err != nil {
		return ""
	}
	var model, arch string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Processor") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && model == "" {
				model = strings.TrimSpace(parts[1])
			}
		}
		if strings.HasPrefix(line, "CPU architecture") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && arch == "" {
				arch = "ARMv" + strings.TrimSpace(parts[1])
			}
		}
	}
	if model != "" {
		return model
	}
	return arch
}

// IsCriticalProcess reports whether killing the process with the given
// process name and exe path is unsafe. Used by both the process monitor
// and the ports scanner to gate process termination.
//
// The set of names mirrors the union of what procmon and ports used to
// keep separately so that all kill paths (KillProcess, port-driven kill,
// process-row kill) agree on what must not be terminated.
func IsCriticalProcess(name, exe string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "ndm", "dropbear", "init", "systemd", "kthreadd", "awg-manager",
		"sshd", "klogd", "syslogd":
		return true
	}
	exeLower := strings.ToLower(exe)
	if exeLower == "" {
		return false
	}
	switch {
	case strings.Contains(exeLower, "/usr/sbin/ndm"),
		strings.Contains(exeLower, "dropbear"),
		strings.Contains(exeLower, "awg-manager"):
		return true
	}
	return false
}
