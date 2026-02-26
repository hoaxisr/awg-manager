package testing

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
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
