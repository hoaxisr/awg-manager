package rci

import (
	"context"
	"encoding/json"
)

// Batch accumulates RCI commands and executes them as a single HTTP POST.
// NDMS processes batch commands sequentially in array order.
type Batch struct {
	commands []any
}

func NewBatch() *Batch {
	return &Batch{}
}

func (b *Batch) Add(cmd any) *Batch {
	b.commands = append(b.commands, cmd)
	return b
}

func (b *Batch) Len() int { return len(b.commands) }
func (b *Batch) Reset()   { b.commands = nil }

func (b *Batch) Execute(ctx context.Context, c *Client) error {
	if len(b.commands) == 0 {
		return nil
	}
	_, err := c.PostBatch(ctx, b.commands)
	return err
}

func (b *Batch) ExecuteWithResults(ctx context.Context, c *Client) ([]json.RawMessage, error) {
	if len(b.commands) == 0 {
		return nil, nil
	}
	return c.PostBatch(ctx, b.commands)
}

// --- Fluent methods ---

func (b *Batch) InterfaceCreate(name string) *Batch               { return b.Add(CmdInterfaceCreate(name)) }
func (b *Batch) InterfaceDelete(name string) *Batch               { return b.Add(CmdInterfaceDelete(name)) }
func (b *Batch) InterfaceDescription(name, desc string) *Batch    { return b.Add(CmdInterfaceDescription(name, desc)) }
func (b *Batch) InterfaceSecurityLevel(name, level string) *Batch { return b.Add(CmdInterfaceSecurityLevel(name, level)) }
func (b *Batch) InterfaceUp(name string, up bool) *Batch          { return b.Add(CmdInterfaceUp(name, up)) }

func (b *Batch) InterfaceIPAddress(name, addr, mask string) *Batch { return b.Add(CmdInterfaceIPAddress(name, addr, mask)) }
func (b *Batch) InterfaceMTU(name string, mtu int) *Batch          { return b.Add(CmdInterfaceMTU(name, mtu)) }
func (b *Batch) InterfaceAdjustMSS(name string, enable bool) *Batch { return b.Add(CmdInterfaceAdjustMSS(name, enable)) }
func (b *Batch) InterfaceIPGlobal(name string, auto bool) *Batch   { return b.Add(CmdInterfaceIPGlobal(name, auto)) }

func (b *Batch) InterfaceDNS(name string, servers []string) *Batch { return b.Add(CmdInterfaceDNS(name, servers)) }
func (b *Batch) InterfaceDNSClear(name string) *Batch              { return b.Add(CmdInterfaceDNSClear(name)) }

func (b *Batch) InterfaceIPv6Address(name, addr string) *Batch    { return b.Add(CmdInterfaceIPv6Address(name, addr)) }
func (b *Batch) InterfaceIPv6AddressClear(name string) *Batch     { return b.Add(CmdInterfaceIPv6AddressClear(name)) }

func (b *Batch) WireguardPrivateKey(name, key string) *Batch       { return b.Add(CmdWireguardPrivateKey(name, key)) }
func (b *Batch) WireguardPeer(name string, peer PeerConfig) *Batch { return b.Add(CmdWireguardPeer(name, peer)) }
func (b *Batch) WireguardPeerDelete(name, pk string) *Batch        { return b.Add(CmdWireguardPeerDelete(name, pk)) }
func (b *Batch) WireguardPeerEndpoint(name, pk, ep string) *Batch  { return b.Add(CmdWireguardPeerEndpoint(name, pk, ep)) }
func (b *Batch) WireguardPeerConnect(name, pk, via string) *Batch  { return b.Add(CmdWireguardPeerConnect(name, pk, via)) }

func (b *Batch) SetDefaultRoute(name string) *Batch        { return b.Add(CmdSetDefaultRoute(name)) }
func (b *Batch) RemoveDefaultRoute(name string) *Batch      { return b.Add(CmdRemoveDefaultRoute(name)) }
func (b *Batch) SetIPv6DefaultRoute(name string) *Batch     { return b.Add(CmdSetIPv6DefaultRoute(name)) }
func (b *Batch) RemoveIPv6DefaultRoute(name string) *Batch  { return b.Add(CmdRemoveIPv6DefaultRoute(name)) }
func (b *Batch) RemoveIPv6HostRoute(host string) *Batch     { return b.Add(CmdRemoveIPv6HostRoute(host)) }

func (b *Batch) Save() *Batch { return b.Add(CmdSave()) }
