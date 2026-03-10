package ovn

import (
	"fmt"
	"strings"
	"testing"
)

func TestMetaKey(t *testing.T) {
	key := MetaKey("my-endpoint-id", MetaKeyMAC)
	expected := "docker:endpoint:my-endpoint-id:mac"
	if key != expected {
		t.Errorf("MetaKey = %q, want %q", key, expected)
	}
}

func TestSwitchAndPortName(t *testing.T) {
	switchID := "abcdef1234567890"
	expectedSwitch := fmt.Sprintf("%s%s", SwitchNamePrefix, switchID[:12])
	if got := SwitchName(switchID); got != expectedSwitch {
		t.Fatalf("SwitchName = %q want %q", got, expectedSwitch)
	}

	endpointID := "0123456789abcdef"
	networkID := "fedcba9876543210"
	port := PortName(endpointID, networkID)
	if !strings.HasPrefix(port, PortNamePrefix) {
		t.Fatalf("PortName should start with %q, got %s", PortNamePrefix, port)
	}
	if !strings.Contains(port, SwitchNamePrefix) {
		t.Fatalf("PortName %s missing switch prefix %s", port, SwitchNamePrefix)
	}
}
