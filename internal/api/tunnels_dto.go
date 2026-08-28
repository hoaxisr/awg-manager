package api

import (
	"regexp"
)

// TunnelPingCheckStatus is the ping-check status embedded in TunnelListItem.
type TunnelPingCheckStatus struct {
	Status        string `json:"status" example:"alive"`
	RestartCount  int    `json:"restartCount" example:"0"`
	FailCount     int    `json:"failCount" example:"0"`
	FailThreshold int    `json:"failThreshold" example:"3"`
}

// TunnelListItemDTO mirrors frontend TunnelListItem.
type TunnelListItemDTO struct {
	ID                        string                `json:"id" example:"tun_abc123"`
	Name                      string                `json:"name" example:"My AWG Tunnel"`
	Type                      string                `json:"type" example:"awg" enums:"awg,wg"`
	Status                    string                `json:"status" example:"connected" enums:"connected,disconnected,error,disabled"`
	Enabled                   bool                  `json:"enabled" example:"true"`
	DefaultRoute              bool                  `json:"defaultRoute" example:"false"`
	Endpoint                  string                `json:"endpoint" example:"vpn.example.com:51820"`
	Address                   string                `json:"address" example:"10.0.0.2/32"`
	InterfaceName             string                `json:"interfaceName,omitempty" example:"nwg0"`
	NdmsName                  string                `json:"ndmsName,omitempty" example:"Wireguard0"`
	Backend                   string                `json:"backend,omitempty" example:"nativewg" enums:"nativewg,kernel"`
	AWGVersion                string                `json:"awgVersion,omitempty" example:"awg2.0" enums:"wg,awg1.0,awg1.5,awg2.0,awg3,awg3.1"`
	MTU                       int                   `json:"mtu,omitempty" example:"1420"`
	IspInterface              string                `json:"ispInterface,omitempty" example:"PPPoE0"`
	IspInterfaceLabel         string                `json:"ispInterfaceLabel,omitempty" example:"WAN"`
	ResolvedIspInterface      string                `json:"resolvedIspInterface,omitempty" example:"PPPoE0"`
	ResolvedIspInterfaceLabel string                `json:"resolvedIspInterfaceLabel,omitempty" example:"WAN"`
	HasAddressConflict        bool                  `json:"hasAddressConflict,omitempty" example:"false"`
	StartedAt                 string                `json:"startedAt,omitempty" example:"2024-01-15T10:00:00Z"`
	RxBytes                   int64                 `json:"rxBytes,omitempty" example:"10485760"`
	TxBytes                   int64                 `json:"txBytes,omitempty" example:"5242880"`
	LastHandshake             string                `json:"lastHandshake,omitempty" example:"2024-01-15T10:30:00Z"`
	PingCheck                 TunnelPingCheckStatus `json:"pingCheck"`
	WdttClientID              string                `json:"wdttClientId,omitempty" example:"default"`
}

// TunnelListResponse is the envelope for GET /tunnels/list.
type TunnelListResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    []TunnelListItemDTO `json:"data"`
}

// TunnelsAllSnapshotData is the payload for GET /tunnels/all.
type TunnelsAllSnapshotData struct {
	Tunnels  []TunnelListItemDTO `json:"tunnels"`
	External []ExternalTunnelDTO `json:"external"`
	System   []SystemTunnelDTO   `json:"system"`
}

// TunnelsAllResponse is the envelope for GET /tunnels/all.
type TunnelsAllResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    TunnelsAllSnapshotData `json:"data"`
}

// AWGInterfaceDTO mirrors frontend AWGInterface.
type AWGInterfaceDTO struct {
	PrivateKey string `json:"privateKey" example:"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="`
	Address    string `json:"address" example:"10.0.0.2/32"`
	MTU        int    `json:"mtu" example:"1420"`
	DNS        string `json:"dns,omitempty" example:"8.8.8.8"`
	Jc         int    `json:"jc" example:"4"`
	Jmin       int    `json:"jmin" example:"40"`
	Jmax       int    `json:"jmax" example:"70"`
	S1         int    `json:"s1" example:"0"`
	S2         int    `json:"s2" example:"0"`
	S3         int    `json:"s3" example:"0"`
	S4         int    `json:"s4" example:"0"`
	H1         string `json:"h1" example:"1"`
	H2         string `json:"h2" example:"2"`
	H3         string `json:"h3" example:"3"`
	H4         string `json:"h4" example:"4"`
}

// AWGPeerDTO mirrors frontend AWGPeer.
type AWGPeerDTO struct {
	PublicKey  string   `json:"publicKey" example:"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="`
	Endpoint   string   `json:"endpoint" example:"vpn.example.com:51820"`
	AllowedIPs []string `json:"allowedIPs" example:"0.0.0.0/0"`
	// PersistentKeepalive — секунды. Одиночное значение приходит числом, а
	// диапазон AWG 3.0 ("min-max") строкой: число сохранено ради tunnels.json,
	// который в этой форме читают и предыдущие версии.
	PersistentKeepalive any `json:"persistentKeepalive,omitempty" swaggertype:"object"`
}

// TunnelStateInfoDTO mirrors frontend TunnelStateInfo.
type TunnelStateInfoDTO struct {
	State          int    `json:"state" example:"3"`
	InterfaceUp    bool   `json:"interfaceUp" example:"true"`
	ProcessRunning bool   `json:"processRunning" example:"true"`
	HasHandshake   bool   `json:"hasHandshake" example:"true"`
	LastHandshake  string `json:"lastHandshake" example:"2024-01-15T10:30:00Z"`
	RxBytes        int64  `json:"rxBytes" example:"10485760"`
	TxBytes        int64  `json:"txBytes" example:"5242880"`
}

// AWGTunnelDTO mirrors frontend AWGTunnel.
type AWGTunnelDTO struct {
	ID            string              `json:"id" example:"tun_abc123"`
	Name          string              `json:"name" example:"My VPN"`
	Type          string              `json:"type" example:"awg" enums:"awg,wg"`
	Enabled       bool                `json:"enabled" example:"true"`
	DefaultRoute  bool                `json:"defaultRoute" example:"false"`
	InterfaceName string              `json:"interfaceName,omitempty" example:"nwg0"`
	State         string              `json:"state,omitempty" example:"connected"`
	Backend       string              `json:"backend,omitempty" example:"nativewg"`
	Interface     AWGInterfaceDTO     `json:"interface"`
	Peer          AWGPeerDTO          `json:"peer"`
	StateInfo     *TunnelStateInfoDTO `json:"stateInfo,omitempty"`
}

// TunnelDetailResponse is the envelope for GET /tunnels/get.
type TunnelDetailResponse struct {
	Success bool         `json:"success" example:"true"`
	Data    AWGTunnelDTO `json:"data"`
}

// TunnelTrafficPoint is one point in a traffic chart.
type TunnelTrafficPoint struct {
	T  int64 `json:"t" example:"1705312200"`
	Rx int64 `json:"rx" example:"1024000"`
	Tx int64 `json:"tx" example:"512000"`
}

// TunnelTrafficStats are aggregate stats for a traffic period.
type TunnelTrafficStats struct {
	Points    int     `json:"points" example:"60"`
	PeakRate  float64 `json:"peakRate" example:"1048576"`
	AvgRx     float64 `json:"avgRx" example:"524288"`
	AvgTx     float64 `json:"avgTx" example:"262144"`
	CurrentRx float64 `json:"currentRx" example:"102400"`
	CurrentTx float64 `json:"currentTx" example:"51200"`
	// VolumeRx is estimated bytes received over the selected window (Σ rxRate×Δt on raw history samples).
	VolumeRx int64 `json:"volumeRx" example:"1073741824"`
	// VolumeTx is estimated bytes sent over the selected window (Σ txRate×Δt on raw history samples).
	VolumeTx int64 `json:"volumeTx" example:"536870912"`
}

// TunnelTrafficData is the payload for GET /tunnels/traffic.
type TunnelTrafficData struct {
	Points []TunnelTrafficPoint `json:"points"`
	Stats  TunnelTrafficStats   `json:"stats"`
}

// TunnelTrafficResponse is the envelope for GET /tunnels/traffic.
type TunnelTrafficResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    TunnelTrafficData `json:"data"`
}

// TunnelDeleteResultData mirrors frontend DeleteResult.
type TunnelDeleteResultData struct {
	Success  bool   `json:"success" example:"true"`
	TunnelId string `json:"tunnelId" example:"tun_abc123"`
	Verified bool   `json:"verified" example:"true"`
}

// TunnelDeleteResponse is the envelope for POST /tunnels/delete.
type TunnelDeleteResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    TunnelDeleteResultData `json:"data"`
}

// TunnelReferencedDetails describes where a tunnel is still referenced
// when deletion is refused (HTTP 409).
type TunnelReferencedDetails struct {
	TunnelID    string   `json:"tunnelId" example:"tun-a"`
	DeviceProxy bool     `json:"deviceProxy" example:"false"`
	RouterRules []int    `json:"routerRules"`
	RouterOther []string `json:"routerOther"`
}

// TunnelReferencedResponse is the HTTP 409 body for POST /tunnels/delete
// when the tunnel's outbound tag is still referenced by sing-box router
// or device-proxy config. The client uses details to deeplink the user
// to the referencing configuration.
type TunnelReferencedResponse struct {
	Error   string                  `json:"error" example:"tunnel_referenced"`
	Details TunnelReferencedDetails `json:"details"`
}

// validTunnelID matches safe tunnel identifiers: starts with a letter,
// followed by up to 31 alphanumeric characters, hyphens, or underscores.
var validTunnelID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,31}$`)

// isValidTunnelID reports whether id is a safe tunnel identifier.
func isValidTunnelID(id string) bool {
	return validTunnelID.MatchString(id)
}
