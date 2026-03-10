package network

import (
	"strings"
	"testing"
)

func TestGenerateMAC(t *testing.T) {
	tests := []struct {
		name       string
		endpointID string
	}{
		{"standard UUID", "550e8400-e29b-41d4-a716-446655440000"},
		{"another UUID", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"},
		{"short id", "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mac := GenerateMAC(tt.endpointID)
			parts := strings.Split(mac, ":")
			if len(parts) != 6 {
				t.Errorf("expected 6 MAC octets, got %d: %s", len(parts), mac)
			}
			if parts[0] != "02" {
				t.Errorf("expected first octet 02, got %s", parts[0])
			}
		})
	}

	mac1 := GenerateMAC("550e8400-e29b-41d4-a716-446655440000")
	mac2 := GenerateMAC("550e8400-e29b-41d4-a716-446655440001")
	if mac1 == mac2 {
		t.Errorf("different endpoint IDs produced the same MAC: %s", mac1)
	}

	mac3 := GenerateMAC("550e8400-e29b-41d4-a716-446655440000")
	if mac1 != mac3 {
		t.Errorf("same endpoint ID produced different MACs: %s vs %s", mac1, mac3)
	}
}

func TestVethName(t *testing.T) {
	name := VethName("550e8400-e29b-41d4-a716-446655440000")
	if !strings.HasPrefix(name, DefaultVethPrefix) {
		t.Errorf("vethName must start with %q, got %s", DefaultVethPrefix, name)
	}
	if len(name) > 15 {
		t.Errorf("vethName exceeds Linux 15-char limit: len=%d name=%s", len(name), name)
	}

	n1 := VethName("aaa")
	n2 := VethName("bbb")
	if n1 == n2 {
		t.Errorf("different IDs produced the same veth name: %s", n1)
	}

	n3 := VethName("aaa")
	if n1 != n3 {
		t.Errorf("same ID produced different veth names: %s vs %s", n1, n3)
	}
}

func TestAddressHasIP(t *testing.T) {
	tests := []struct {
		address  string
		ip       string
		expected bool
	}{
		{"02:ac:10:ff:01:01 10.0.0.1", "10.0.0.1", true},
		{"02:ac:10:ff:01:01 10.0.0.1", "10.0.0.2", false},
		{"10.0.0.1", "10.0.0.1", true},
		{"10.0.0.1", "10.0.0.2", false},
		{"", "10.0.0.1", false},
		{"02:ac:10:ff:01:01 10.0.0.1 10.0.0.2", "10.0.0.2", true},
	}

	for _, tt := range tests {
		t.Run(tt.address+"/"+tt.ip, func(t *testing.T) {
			got := AddressHasIP(tt.address, tt.ip)
			if got != tt.expected {
				t.Errorf("addressHasIP(%q, %q) = %v, want %v",
					tt.address, tt.ip, got, tt.expected)
			}
		})
	}
}
