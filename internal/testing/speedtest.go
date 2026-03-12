package testing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

const speedTestTimeout = 25 * time.Second

// defaultServers is a hardcoded list of public iperf3 servers.
var defaultServers = []SpeedTestServer{
	// Russia
	{Label: "Москва, RU (Hostkey)", Host: "speedtest.hostkey.ru", Port: 5201},
	{Label: "Москва, RU (МТС)", Host: "mskst.st.mtsws.net", Port: 3333},
	// Northern Europe
	{Label: "Хельсинки, FI (Hostkey)", Host: "spd-fisrv.hostkey.com", Port: 5201},
	{Label: "Стокгольм, SE", Host: "speedtest.kamel.network", Port: 5201},
	{Label: "Копенгаген, DK", Host: "speedtest.hiper.dk", Port: 5201},
	// Western Europe
	{Label: "Амстердам, NL (Leaseweb)", Host: "speedtest.ams1.nl.leaseweb.net", Port: 5201},
	{Label: "Амстердам, NL (Clouvider)", Host: "ams.speedtest.clouvider.net", Port: 5200},
	{Label: "Лондон, UK (Leaseweb)", Host: "speedtest.lon1.uk.leaseweb.net", Port: 5201},
	{Label: "Лондон, UK (Clouvider)", Host: "lon.speedtest.clouvider.net", Port: 5200},
	{Label: "Париж, FR (Scaleway)", Host: "ping.online.net", Port: 5200},
	{Label: "Париж, FR (MilkyWan)", Host: "speedtest.milkywan.fr", Port: 9200},
	// Central Europe
	{Label: "Франкфурт, DE (Leaseweb)", Host: "speedtest.fra1.de.leaseweb.net", Port: 5201},
	{Label: "Франкфурт, DE (Clouvider)", Host: "fra.speedtest.clouvider.net", Port: 5200},
	{Label: "Берлин, DE (Wobcom)", Host: "a209.speedtest.wobcom.de", Port: 5201},
	{Label: "Цюрих, CH (iWay)", Host: "speedtest.iway.ch", Port: 5201},
	{Label: "Вена, AT (Alwyzon)", Host: "lg.vie.alwyzon.net", Port: 5201},
	// Southern Europe
	{Label: "Италия, IT (Aruba)", Host: "it1.speedtest.aruba.it", Port: 5201},
	{Label: "Лиссабон, PT (NOS)", Host: "lisboa.speedtest.net.zon.pt", Port: 5201},
	// Other
	{Label: "Рейкьявик, IS (Hostkey)", Host: "spd-icsrv.hostkey.com", Port: 5201},
	{Label: "Нью-Йорк, US", Host: "nyc.iperf.express", Port: 5201},
}

// GetSpeedTestInfo checks iperf3 availability and returns the server list.
func (s *Service) GetSpeedTestInfo() *SpeedTestInfo {
	_, err := exec.LookPath("iperf3")
	return &SpeedTestInfo{
		Available: err == nil,
		Servers:   defaultServers,
	}
}

// SpeedTest runs iperf3 through the tunnel in the given direction.
func (s *Service) SpeedTest(ctx context.Context, tunnelID, server string, port int, direction string) (*SpeedTestResult, error) {
	if err := s.CheckTunnelRunning(tunnelID); err != nil {
		return nil, err
	}

	ifaceName := tunnel.NewNames(tunnelID).IfaceName

	args := []string{
		"-c", server,
		"-p", strconv.Itoa(port),
		"-t", "10",
		"-J",
		"--bind-dev", ifaceName,
	}
	if direction == "download" {
		args = append(args, "-R")
	}

	result, err := sysexec.RunWithOptions(ctx, "iperf3", args, sysexec.Options{
		Timeout: speedTestTimeout,
	})
	if err != nil {
		errMsg := sysexec.FormatError(result, err).Error()
		return nil, fmt.Errorf("iperf3 failed: %s", errMsg)
	}

	return parseIperf3Result(result.Stdout, server, direction)
}

// SpeedTestInterval represents a single-second measurement from iperf3.
type SpeedTestInterval struct {
	Second    int     `json:"second"`
	Bandwidth float64 `json:"bandwidth"` // Mbps
}

// SpeedTestStream runs iperf3 in text mode and calls onInterval for each second.
// Returns the final summary result.
func (s *Service) SpeedTestStream(ctx context.Context, tunnelID, server string, port int, direction string, onInterval func(SpeedTestInterval)) (*SpeedTestResult, error) {
	if err := s.CheckTunnelRunning(tunnelID); err != nil {
		return nil, err
	}

	ifaceName := tunnel.NewNames(tunnelID).IfaceName

	args := []string{
		"-c", server,
		"-p", strconv.Itoa(port),
		"-t", "10",
		"--forceflush",
		"--bind-dev", ifaceName,
	}
	if direction == "download" {
		args = append(args, "-R")
	}

	cmdCtx, cancel := context.WithTimeout(ctx, speedTestTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "iperf3", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("iperf3 stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("iperf3 start: %w", err)
	}

	var lastBandwidth float64
	var totalBytes int64
	var totalSeconds float64
	var retransmits int
	second := 0

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if bw, retr, isSummary := parseIperf3TextLine(line, direction); bw >= 0 {
			if isSummary {
				lastBandwidth = bw
				retransmits = retr
			} else {
				second++
				lastBandwidth = bw
				if onInterval != nil {
					onInterval(SpeedTestInterval{Second: second, Bandwidth: bw})
				}
			}
		}
	}

	err = cmd.Wait()
	if err != nil {
		return nil, fmt.Errorf("iperf3 failed: %w", err)
	}

	totalSeconds = float64(second)
	if totalSeconds > 0 {
		totalBytes = int64(lastBandwidth * 1e6 / 8 * totalSeconds)
	}

	return &SpeedTestResult{
		Server:      server,
		Direction:   direction,
		Bandwidth:   lastBandwidth,
		Bytes:       totalBytes,
		Duration:    totalSeconds,
		Retransmits: retransmits,
	}, nil
}

// iperf3 text output line pattern:
// [  5]   0.00-1.00   sec   256 KBytes  2.10 Mbits/sec    0    107 KBytes
// [  5]   0.00-10.00  sec  2.50 MBytes  2.10 Mbits/sec    0             sender
var iperf3IntervalRe = regexp.MustCompile(
	`\[\s*\d+\]\s+([\d.]+)-([\d.]+)\s+sec\s+[\d.]+\s+\w+\s+([\d.]+)\s+([KMG]?)bits/sec(?:\s+(\d+))?(?:\s+.*(sender|receiver))?`,
)

// parseIperf3TextLine extracts bandwidth from an iperf3 text output line.
// Returns (bandwidth_mbps, retransmits, is_summary). bandwidth < 0 means line not relevant.
func parseIperf3TextLine(line, direction string) (float64, int, bool) {
	m := iperf3IntervalRe.FindStringSubmatch(line)
	if m == nil {
		return -1, 0, false
	}

	bw, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return -1, 0, false
	}

	// Convert to Mbps
	switch m[4] {
	case "K":
		bw /= 1000
	case "G":
		bw *= 1000
	}

	retr := 0
	if m[5] != "" {
		retr, _ = strconv.Atoi(m[5])
	}

	// Summary line contains "sender" or "receiver"
	isSummary := strings.Contains(line, "sender") || strings.Contains(line, "receiver")

	// For summary: download uses "receiver", upload uses "sender"
	if isSummary {
		if direction == "download" && !strings.Contains(line, "receiver") {
			return -1, 0, false
		}
		if direction == "upload" && !strings.Contains(line, "sender") {
			return -1, 0, false
		}
		return bw, retr, true
	}

	return bw, 0, false
}

// iperf3JSON is the minimal structure for parsing iperf3 -J output.
type iperf3JSON struct {
	End struct {
		SumSent struct {
			Bytes         int64   `json:"bytes"`
			BitsPerSecond float64 `json:"bits_per_second"`
			Retransmits   int     `json:"retransmits"`
			Seconds       float64 `json:"seconds"`
		} `json:"sum_sent"`
		SumReceived struct {
			Bytes         int64   `json:"bytes"`
			BitsPerSecond float64 `json:"bits_per_second"`
			Seconds       float64 `json:"seconds"`
		} `json:"sum_received"`
	} `json:"end"`
	Error string `json:"error"`
}

// parseIperf3Result extracts bandwidth from iperf3 JSON output.
func parseIperf3Result(stdout, server, direction string) (*SpeedTestResult, error) {
	var data iperf3JSON
	if err := json.Unmarshal([]byte(stdout), &data); err != nil {
		return nil, fmt.Errorf("failed to parse iperf3 output: %w", err)
	}

	if data.Error != "" {
		return nil, fmt.Errorf("iperf3 error: %s", data.Error)
	}

	result := &SpeedTestResult{
		Server:    server,
		Direction: direction,
	}

	if direction == "download" {
		sum := data.End.SumReceived
		result.Bandwidth = sum.BitsPerSecond / 1e6
		result.Bytes = sum.Bytes
		result.Duration = sum.Seconds
	} else {
		sum := data.End.SumSent
		result.Bandwidth = sum.BitsPerSecond / 1e6
		result.Bytes = sum.Bytes
		result.Duration = sum.Seconds
		result.Retransmits = sum.Retransmits
	}

	return result, nil
}
