package rci

import (
	"context"
	"encoding/json"
)

func (c *Client) ShowVersion(ctx context.Context) (*VersionInfo, error) {
	var info VersionInfo
	if err := c.Get(ctx, "/show/version", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *Client) ShowInterface(ctx context.Context, name string) (json.RawMessage, error) {
	raw, err := c.GetRaw(ctx, "/show/interface/"+name)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (c *Client) ShowAllInterfaces(ctx context.Context) (map[string]json.RawMessage, error) {
	var result map[string]json.RawMessage
	if err := c.Get(ctx, "/show/interface/", &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) ShowIPRoute(ctx context.Context) ([]RouteEntry, error) {
	var routes []RouteEntry
	if err := c.Get(ctx, "/show/ip/route", &routes); err != nil {
		return nil, err
	}
	return routes, nil
}

func (c *Client) ShowPingCheck(ctx context.Context) (*PingCheckListResponse, error) {
	var resp PingCheckListResponse
	if err := c.Get(ctx, "/show/ping-check/", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ShowRCInterface(ctx context.Context, name string) (json.RawMessage, error) {
	raw, err := c.GetRaw(ctx, "/show/rc/interface/"+name)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (c *Client) ShowRC(ctx context.Context) (json.RawMessage, error) {
	raw, err := c.GetRaw(ctx, "/show/rc")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (c *Client) ShowASCParams(ctx context.Context, name string) (json.RawMessage, error) {
	raw, err := c.GetRaw(ctx, "/show/rc/interface/"+name+"/wireguard/asc")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (c *Client) ShowObjectGroupFQDN(ctx context.Context) (json.RawMessage, error) {
	raw, err := c.GetRaw(ctx, "/show/object-group/fqdn")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (c *Client) ShowDnsProxyRoute(ctx context.Context) (json.RawMessage, error) {
	raw, err := c.GetRaw(ctx, "/show/rc/dns-proxy/route")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (c *Client) GetSystemName(ctx context.Context, ndmsName string) (string, error) {
	var resp struct {
		Name string `json:"name"`
	}
	if err := c.Get(ctx, "/show/interface/system-name?name="+ndmsName, &resp); err != nil {
		return ndmsName, err
	}
	if resp.Name == "" {
		return ndmsName, nil
	}
	return resp.Name, nil
}
