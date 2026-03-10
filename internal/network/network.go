package network

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
)

const (
	DefaultVethPrefix   = "veth"
	DefaultDstPrefix    = "eth"
	ContainerVethSuffix = "_c"
)

// GenerateMAC creates a locally-administered unicast MAC address from an
// endpoint ID using SHA-256 so the result is deterministic and unique.
func GenerateMAC(endpointID string) string {
	sum := sha256.Sum256([]byte(endpointID))
	// Set locally-administered (bit 1) and clear multicast (bit 0) in first octet.
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x",
		sum[0], sum[1], sum[2], sum[3], sum[4])
}

// VethName derives a deterministic, collision-resistant veth host-side name
// from the endpoint ID. Linux interface names are limited to 15 characters;
// "veth" (4) + 8 hex chars from SHA-256 = 12 chars. The container-side name is
// formed by appending "_c", so keeping the host veth to 12 chars ensures the
// container veth stays within the 15-char limit.
func VethName(endpointID string) string {
	sum := sha256.Sum256([]byte(endpointID))
	return fmt.Sprintf("%s%x", DefaultVethPrefix, sum[:4])
}

// AddressHasIP checks if an address string contains a specific IP
func AddressHasIP(address string, ipAddr string) bool {
	if address == ipAddr {
		return true
	}
	parts := strings.Fields(address)
	return slices.Contains(parts, ipAddr)
}
