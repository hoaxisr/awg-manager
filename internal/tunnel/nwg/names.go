package nwg

import (
	"fmt"
	"regexp"
	"strconv"
)

const MaxTunnels = 10

var reNDMSCreated = regexp.MustCompile(`"Wireguard(\d+)" interface created`)

type NWGNames struct {
	Index     int
	NDMSName  string
	IfaceName string
}

func NewNWGNames(index int) NWGNames {
	return NWGNames{
		Index:     index,
		NDMSName:  fmt.Sprintf("Wireguard%d", index),
		IfaceName: fmt.Sprintf("nwg%d", index),
	}
}

func ParseNDMSCreatedName(output string) (index int, ndmsName string, err error) {
	matches := reNDMSCreated.FindStringSubmatch(output)
	if matches == nil {
		return 0, "", fmt.Errorf("nwg: cannot parse NDMS created name from %q", output)
	}
	idx, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, "", fmt.Errorf("nwg: invalid index in %q: %w", output, err)
	}
	return idx, fmt.Sprintf("Wireguard%d", idx), nil
}
