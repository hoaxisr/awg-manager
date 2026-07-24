package routerinfo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

var (
	rciGetJSONFunc  = rciGetJSON
	rciGetRawFunc   = rciGetRaw
	statfsUsageFunc = statfsUsage
)

// RouterDetails contains extended router metadata derived from NDMS/RCI and local procfs.
type RouterDetails struct {
	Model             string   `json:"model,omitempty" example:"KN-3812"`
	ModelDisplay      string   `json:"modelDisplay,omitempty" example:"CMCC RAX3000M (KN-3812)"`
	PortedBuild       bool     `json:"portedBuild" example:"true"`
	HardwareID        string   `json:"hardwareId,omitempty" example:"KN-3812"`
	Region            string   `json:"region,omitempty" example:"EA"`
	Architecture      string   `json:"architecture,omitempty" example:"aarch64"`
	CPUModel          string   `json:"cpuModel,omitempty" example:"MT7981"`
	CPUTempC          int      `json:"cpuTempC,omitempty" example:"79"`
	WiFi24TempC       int      `json:"wifi24TempC,omitempty" example:"70"`
	WiFi5TempC        int      `json:"wifi5TempC,omitempty" example:"68"`
	MemoryUsedMB      int      `json:"memoryUsedMB,omitempty" example:"303"`
	MemoryTotalMB     int      `json:"memoryTotalMB,omitempty" example:"486"`
	MemoryUsedPercent int      `json:"memoryUsedPercent,omitempty" example:"62"`
	FirmwareTitle     string   `json:"firmwareTitle,omitempty" example:"CMCC RAX3000M (KN-3812) [Port]"`
	FirmwareRelease   string   `json:"firmwareRelease,omitempty" example:"5.0.9 (5.00.C.9.0-1)"`
	FirmwareSandbox   string   `json:"firmwareSandbox,omitempty" example:"preview"`
	FirmwareBuildDate string   `json:"firmwareBuildDate,omitempty" example:"7 Apr 2026"`
	BootSlot          string   `json:"bootSlot,omitempty" example:"1"`
	UptimeHuman       string   `json:"uptimeHuman,omitempty" example:"7d 16h 57m"`
	LoadAverage       string   `json:"loadAverage,omitempty" example:"1.82, 1.58, 1.58"`
	OpkgStorage       string   `json:"opkgStorage,omitempty" example:"451 MB / 205 GB"`
	VPNComponents     []string `json:"vpnComponents,omitempty" example:"WireGuard,OpenVPN,IPsec/IKEv2,L2TP,SSTP,ZeroTier"`
	StorageComponents []string `json:"storageComponents,omitempty" example:"NTFS,ExFAT,EXT4,SMB,FTP"`
	FeatureComponents []string `json:"featureComponents,omitempty" example:"HW-NAT,Wi-Fi 5GHz,WPA3,USB"`
	MeshMembers       []string `json:"meshMembers,omitempty" example:"RAX3000M (KN-3812) | 5.0.9 | 1921 Мбит/с | 13 дн. 06:22:26,SmartBox Giga (KN-1913) | 5.0.9 | 260 Мбит/с | 13 дн. 06:21:52"`
}

type rciVersionWire struct {
	Release string `json:"release"`
	Title   string `json:"title"`
	Model   string `json:"model"`
	HwID    string `json:"hw_id"`
	Region  string `json:"region"`
	Arch    string `json:"arch"`
	Sandbox string `json:"sandbox"`
	Ndm     struct {
		CDate string `json:"cdate"`
	} `json:"ndm"`
	NDW struct {
		Components string `json:"components"`
		Features   string `json:"features"`
	} `json:"ndw"`
}

type rciInterfaceTempWire map[string]struct {
	Temperature int `json:"temperature"`
}

type rciOpkgDiskWire struct {
	Disk string `json:"disk"`
}

type rciLSWire map[string]struct {
	Free  int64  `json:"free"`
	Total int64  `json:"total"`
	Label string `json:"label"`
}

type rciLSRootWire struct {
	Entry map[string]json.RawMessage `json:"entry"`
}

// Collect gathers all router details from NDMS, RCI, and local procfs.
func Collect() *RouterDetails {
	ver := ndmsinfo.Get()
	if ver == nil {
		return nil
	}
	out := &RouterDetails{
		Model:           strings.TrimSpace(ver.Model),
		HardwareID:      strings.TrimSpace(ver.HardwareID),
		Region:          strings.TrimSpace(ver.Region),
		FirmwareRelease: strings.TrimSpace(ver.Release),
		FirmwareTitle:   strings.TrimSpace(ver.Title),
	}

	// Run all RCI HTTP calls in parallel to avoid sequential 1500ms timeouts.
	var (
		rciVer     *rciVersionWire
		wifi24     int
		wifi5      int
		opkgStore  string
		meshMember []string
		wg         sync.WaitGroup
		mu         sync.Mutex
	)
	wg.Add(4)
	go func() {
		defer wg.Done()
		v := fetchRCIVersion()
		mu.Lock()
		rciVer = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		w24, w5 := fetchWiFiTemps()
		mu.Lock()
		wifi24, wifi5 = w24, w5
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		s := fetchOPKGStorage()
		mu.Lock()
		opkgStore = s
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		m := fetchMeshMembers()
		mu.Lock()
		meshMember = m
		mu.Unlock()
	}()
	wg.Wait()

	if rciVer != nil {
		if out.Model == "" {
			out.Model = strings.TrimSpace(rciVer.Model)
		}
		if out.HardwareID == "" {
			out.HardwareID = strings.TrimSpace(rciVer.HwID)
		}
		if out.Region == "" {
			out.Region = strings.TrimSpace(rciVer.Region)
		}
		if out.FirmwareRelease == "" {
			out.FirmwareRelease = strings.TrimSpace(rciVer.Release)
		}
		if out.FirmwareTitle == "" {
			out.FirmwareTitle = strings.TrimSpace(rciVer.Title)
		}
		out.Architecture = detectArchitecture(strings.TrimSpace(rciVer.Arch))
		out.FirmwareSandbox = strings.TrimSpace(rciVer.Sandbox)
		out.FirmwareBuildDate = strings.TrimSpace(rciVer.Ndm.CDate)
		out.VPNComponents = detectLabeledComponents(rciVer.NDW.Components, []componentLabel{
			{key: "wireguard", label: "WireGuard"},
			{key: "openvpn", label: "OpenVPN"},
			{key: "ipsec", label: "IPsec/IKEv2"},
			{key: "l2tp", label: "L2TP"},
			{key: "sstp", label: "SSTP"},
			{key: "zerotier", label: "ZeroTier"},
		})
		out.StorageComponents = detectLabeledComponents(rciVer.NDW.Components, []componentLabel{
			{key: "ntfs", label: "NTFS"},
			{key: "exfat", label: "ExFAT"},
			{key: "ext", label: "EXT4"},
			{key: "tsmb", label: "SMB"},
			{key: "ftp", label: "FTP"},
		})
		out.FeatureComponents = detectLabeledComponents(rciVer.NDW.Features, []componentLabel{
			{key: "hwnat", label: "HW-NAT"},
			{key: "ppe", label: "PPE"},
			{key: "wifi5ghz", label: "Wi-Fi 5GHz"},
			{key: "wpa3", label: "WPA3"},
			{key: "usb", label: "USB"},
		})
	}
	if out.Architecture == "" {
		out.Architecture = runtime.GOARCH
	}

	out.ModelDisplay, out.PortedBuild = buildModelDisplay(out.Model)
	out.CPUModel = detectCPUModel()
	out.CPUTempC = readThermalZoneC("/sys/devices/virtual/thermal/thermal_zone0/temp")
	out.WiFi24TempC, out.WiFi5TempC = wifi24, wifi5
	out.MemoryUsedMB, out.MemoryTotalMB, out.MemoryUsedPercent = readMemUsage()
	out.UptimeHuman = formatUptime(readUptimeSeconds())
	out.LoadAverage = readLoadAverage()
	out.BootSlot = strings.TrimSpace(readTextFile("/proc/dual_image/boot_current"))
	out.OpkgStorage = opkgStore
	out.MeshMembers = meshMember

	return out
}

func fetchRCIVersion() *rciVersionWire {
	var v rciVersionWire
	if err := rciGetJSON("/show/version", &v); err != nil {
		return nil
	}
	return &v
}

func rciGetJSON(path string, dst any) error {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:79/rci"+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

func rciGetRaw(path string) ([]byte, error) {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:79/rci"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func buildModelDisplay(model string) (string, bool) {
	m := strings.TrimSpace(model)
	if m == "" {
		return "", false
	}
	vendor := "Keenetic"
	port := false
	rules := []struct {
		patterns []string
		vendor   string
		port     bool
	}{
		{[]string{"Cudy", "WBR3000", "TR3000", "WR3000"}, "Cudy", true},
		{[]string{"CMCC", "RAX3000M"}, "CMCC", true},
		{[]string{"Netis", "NX31", "NX32", "N6"}, "Netis", true},
		{[]string{"Redmi"}, "Redmi", true},
		{[]string{"Xiaomi", "AX3000T", "3G", "3P", "4A", "4C"}, "Xiaomi", true},
		{[]string{"Mercusys"}, "Mercusys", true},
		{[]string{"SmartBox"}, "SmartBox", true},
		{[]string{"TP-Link", "EC330", "Archer"}, "TP-Link", true},
		{[]string{"Linksys"}, "Linksys", true},
		{[]string{"WiFire"}, "WiFire", true},
		{[]string{"Vertell"}, "Vertell", true},
		{[]string{"MTS", "WG430"}, "MTS", true},
		{[]string{"HLK"}, "HLK", true},
	}
	for _, r := range rules {
		for _, p := range r.patterns {
			if strings.Contains(m, p) {
				vendor = r.vendor
				port = r.port
				goto done
			}
		}
	}
done:
	display := m
	if vendor != "" && vendor != "Keenetic" && !strings.Contains(m, vendor) {
		display = vendor + " " + m
	}
	return display, port
}

var cpuModelPattern = regexp.MustCompile(`MT76[0-9A-Za-z]*|MT79[0-9A-Za-z]*|EN75[0-9A-Za-z]*`)

// CPU model cannot change while the process runs, and detecting it means
// regexp-scanning a multi-MB system library — compute once per process.
var (
	cpuModelOnce sync.Once
	cpuModelVal  string
)

func detectCPUModel() string {
	cpuModelOnce.Do(func() { cpuModelVal = detectCPUModelUncached() })
	return cpuModelVal
}

func detectCPUModelUncached() string {
	b, err := os.ReadFile("/lib/libndmMwsController.so")
	if err == nil {
		if m := cpuModelPattern.Find(b); len(m) > 0 {
			return string(m)
		}
	}
	proc := readTextFile("/proc/cpuinfo")
	for _, line := range strings.Split(proc, "\n") {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		if strings.HasPrefix(lower, "system type") || strings.HasPrefix(lower, "hardware") || strings.HasPrefix(lower, "model name") {
			parts := strings.SplitN(trim, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func detectArchitecture(fallback string) string {
	arch := strings.TrimSpace(fallback)
	if arch != "" {
		return arch
	}
	return runtime.GOARCH
}

func fetchWiFiTemps() (int, int) {
	var payload rciInterfaceTempWire
	if err := rciGetJSON("/show/interface", &payload); err != nil {
		return 0, 0
	}
	return payload["WifiMaster0"].Temperature, payload["WifiMaster1"].Temperature
}

func readThermalZoneC(path string) int {
	raw := strings.TrimSpace(readTextFile(path))
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	if v >= 1000 {
		return v / 1000
	}
	return v
}

func readMemUsage() (usedMB int, totalMB int, usedPercent int) {
	meminfo := readTextFile("/proc/meminfo")
	var totalKB, availKB int
	for _, line := range strings.Split(meminfo, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			totalKB = parseFirstInt(line)
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			availKB = parseFirstInt(line)
		}
	}
	if totalKB <= 0 {
		return 0, 0, 0
	}
	totalMB = totalKB / 1024
	usedMB = (totalKB - availKB) / 1024
	if totalMB > 0 {
		usedPercent = usedMB * 100 / totalMB
	}
	return
}

func parseFirstInt(s string) int {
	fields := strings.Fields(s)
	for _, f := range fields {
		if n, err := strconv.Atoi(f); err == nil {
			return n
		}
	}
	return 0
}

func readUptimeSeconds() int64 {
	raw := strings.TrimSpace(readTextFile("/proc/uptime"))
	if raw == "" {
		return 0
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	intPart := strings.SplitN(fields[0], ".", 2)[0]
	v, _ := strconv.ParseInt(intPart, 10, 64)
	return v
}

func formatUptime(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	return fmt.Sprintf("%dd %dh %dm", d, h, m)
}

func readLoadAverage() string {
	raw := strings.TrimSpace(readTextFile("/proc/loadavg"))
	if raw == "" {
		return ""
	}
	f := strings.Fields(raw)
	if len(f) < 3 {
		return ""
	}
	return strings.Join(f[:3], ", ")
}

func fetchOPKGStorage() string {
	if s := fetchOPKGStorageFromRCI(); s != "" {
		return s
	}
	return fetchStorageByPath("/opt")
}

func fetchOPKGStorageFromRCI() string {
	var disk rciOpkgDiskWire
	if err := rciGetJSONFunc("/show/sc/opkg/disk", &disk); err != nil {
		return ""
	}
	rawLabel := strings.TrimSpace(disk.Disk)
	if rawLabel == "" {
		return ""
	}

	// kn-info parity: it keeps leading path markers and only strips trailing "/" and ":".
	knLabel := strings.TrimSuffix(strings.TrimSuffix(rawLabel, "/"), ":")
	trimmedLabel := strings.Trim(rawLabel, ":/")
	labels := make([]string, 0, 4)
	for _, v := range []string{rawLabel, knLabel, trimmedLabel} {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		dup := false
		for _, seen := range labels {
			if seen == v {
				dup = true
				break
			}
		}
		if !dup {
			labels = append(labels, v)
		}
	}
	if len(labels) == 0 {
		return ""
	}

	raw, err := rciGetRawFunc("/ls")
	if err != nil {
		return ""
	}

	// Newer NDMS layout: {"entry": {"<id>:": {...}}}
	var root rciLSRootWire
	if err := json.Unmarshal(raw, &root); err == nil && len(root.Entry) > 0 {
		for _, nodeRaw := range root.Entry {
			var node map[string]any
			if err := json.Unmarshal(nodeRaw, &node); err != nil {
				continue
			}
			nodeLabel := strings.TrimSpace(anyToString(node["label"]))
			for _, label := range labels {
				if nodeLabel != label && strings.Trim(nodeLabel, ":/") != strings.Trim(label, ":/") {
					continue
				}
				free := anyToInt64(node["free"])
				total := anyToInt64(node["total"])
				if total <= 0 {
					continue
				}
				used := total - free
				return formatBytesPair(used, total)
			}
		}
	}

	var ls rciLSWire
	if err := json.Unmarshal(raw, &ls); err == nil {
		for _, label := range labels {
			key := label + ":"
			if v, ok := ls[key]; ok && v.Total > 0 {
				used := v.Total - v.Free
				return formatBytesPair(used, v.Total)
			}
		}
	}
	// kn-info parity fallback: locate matching "label" block in /rci/ls response.
	var anyMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &anyMap); err != nil {
		return ""
	}
	for _, nodeRaw := range anyMap {
		var node struct {
			Label string `json:"label"`
			Free  int64  `json:"free"`
			Total int64  `json:"total"`
		}
		if err := json.Unmarshal(nodeRaw, &node); err != nil {
			continue
		}
		nodeLabel := strings.TrimSpace(node.Label)
		for _, label := range labels {
			if node.Total > 0 && (nodeLabel == label || strings.Trim(nodeLabel, ":/") == strings.Trim(label, ":/")) {
				used := node.Total - node.Free
				return formatBytesPair(used, node.Total)
			}
		}
	}
	return ""
}

func fetchStorageByPath(path string) string {
	used, total, ok := statfsUsageFunc(path)
	if !ok || total <= 0 || used < 0 {
		return ""
	}
	return formatBytesPair(used, total)
}

// FreeBytes returns the free space (bytes) on the filesystem containing path.
// ok is false when the filesystem can't be queried (e.g. unsupported platform);
// callers should fall back to a static bound in that case.
func FreeBytes(path string) (free int64, ok bool) {
	used, total, ok := statfsUsageFunc(path)
	if !ok {
		return 0, false
	}
	free = total - used
	if free < 0 {
		free = 0
	}
	return free, true
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	default:
		return ""
	}
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

func formatBytesPair(used, total int64) string {
	usedMB := used / 1024 / 1024
	totalMB := total / 1024 / 1024
	if totalMB >= 1024 {
		totalGB := total / 1024 / 1024 / 1024
		if usedMB < 1024 {
			return fmt.Sprintf("%d MB / %d GB", usedMB, totalGB)
		}
		return fmt.Sprintf("%d GB / %d GB", used/1024/1024/1024, totalGB)
	}
	return fmt.Sprintf("%d MB / %d MB", usedMB, totalMB)
}

func fetchMeshMembers() []string {
	var members []map[string]any
	if err := rciGetJSON("/show/mws/member", &members); err != nil {
		return nil
	}
	out := make([]string, 0, len(members))
	for _, m := range members {
		model := strings.TrimSpace(anyToString(m["model"]))
		if model == "" {
			continue
		}
		fw := strings.TrimSpace(anyToString(m["fw"]))
		if fw == "" {
			out = append(out, model+" | Не в сети")
			continue
		}

		var uptime int64
		if sys, ok := m["system"].(map[string]any); ok {
			uptime = anyToInt64(sys["uptime"])
		}

		var speed int64
		if backhaul, ok := m["backhaul"].(map[string]any); ok {
			speed = anyToInt64(backhaul["txrate"])
			if speed <= 0 {
				speed = anyToInt64(backhaul["speed"])
			}
		}
		if speed <= 0 {
			speed = 0
		}
		out = append(out, fmt.Sprintf("%s | %s | %d Мбит/с | %s", model, fw, speed, formatUptimeRU(uptime)))
	}
	return out
}

func formatUptimeRU(seconds int64) string {
	if seconds <= 0 {
		return "00:00:00"
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	mins := (seconds % 3600) / 60
	secs := seconds % 60
	if days > 0 {
		return fmt.Sprintf("%d дн. %02d:%02d:%02d", days, hours, mins, secs)
	}
	return fmt.Sprintf("%02d:%02d:%02d", hours, mins, secs)
}

type componentLabel struct {
	key   string
	label string
}

func detectLabeledComponents(raw string, labels []componentLabel) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	lower := strings.ToLower(raw)
	out := make([]string, 0, len(labels))
	for _, item := range labels {
		if strings.Contains(lower, item.key) {
			out = append(out, item.label)
		}
	}
	return out
}

func readTextFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(bytes.TrimSpace(b)))
}
