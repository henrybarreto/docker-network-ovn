package ipam

import (
	"fmt"
	"net"
	"strings"

	"github.com/docker/go-plugins-helpers/network"
)

func ValidateIPv4Data(ipv4 []*network.IPAMData, ipv6 []*network.IPAMData) (string, string, error) {
	if len(ipv6) > 0 {
		return "", "", fmt.Errorf("IPv6 is not supported")
	}
	if len(ipv4) == 0 {
		return "", "", fmt.Errorf("subnet not specified")
	}
	if len(ipv4) > 1 {
		return "", "", fmt.Errorf("multiple IPv4 subnets not supported")
	}
	if ipv4[0] == nil {
		return "", "", fmt.Errorf("subnet not specified")
	}
	subnet := ipv4[0].Pool
	if subnet == "" {
		return "", "", fmt.Errorf("subnet not specified")
	}
	if _, _, err := net.ParseCIDR(subnet); err != nil {
		return "", "", fmt.Errorf("invalid subnet CIDR %q: %w", subnet, err)
	}
	gateway, err := NormalizeGateway(ipv4[0].Gateway)
	if err != nil {
		return "", "", err
	}
	return subnet, gateway, nil
}

func NormalizeGateway(gw string) (string, error) {
	if gw == "" {
		return "", nil
	}
	if strings.Contains(gw, "/") {
		ip, _, err := net.ParseCIDR(gw)
		if err != nil {
			return "", fmt.Errorf("invalid gateway address: %w", err)
		}
		return ip.String(), nil
	}
	if net.ParseIP(gw) == nil {
		return "", fmt.Errorf("invalid gateway address: %s", gw)
	}
	return gw, nil
}
