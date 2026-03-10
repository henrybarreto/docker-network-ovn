package ovn

import "fmt"

const (
	SwitchNamePrefix = "ls-"
	PortNamePrefix   = "lsp-"
	MetaKeyFormat    = "docker:endpoint:%s:%s"
	MetaKeyMAC       = "mac"
	MetaKeyIP        = "ip"
)

func SwitchName(networkID string) string {
	return fmt.Sprintf("%s%s", SwitchNamePrefix, networkID[:12])
}

func PortName(endpointID, networkID string) string {
	return fmt.Sprintf("%s%s-%s%s", PortNamePrefix, endpointID[:12], SwitchNamePrefix, networkID[:12])
}

func MetaKey(endpointID string, suffix string) string {
	return fmt.Sprintf(MetaKeyFormat, endpointID, suffix)
}
