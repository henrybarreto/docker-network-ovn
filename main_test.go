package main

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
			mac := generateMAC(tt.endpointID)
			parts := strings.Split(mac, ":")
			if len(parts) != 6 {
				t.Errorf("expected 6 MAC octets, got %d: %s", len(parts), mac)
			}
			if parts[0] != "02" {
				t.Errorf("expected first octet 02, got %s", parts[0])
			}
		})
	}

	mac1 := generateMAC("550e8400-e29b-41d4-a716-446655440000")
	mac2 := generateMAC("550e8400-e29b-41d4-a716-446655440001")
	if mac1 == mac2 {
		t.Errorf("different endpoint IDs produced the same MAC: %s", mac1)
	}

	mac3 := generateMAC("550e8400-e29b-41d4-a716-446655440000")
	if mac1 != mac3 {
		t.Errorf("same endpoint ID produced different MACs: %s vs %s", mac1, mac3)
	}
}

func TestVethName(t *testing.T) {
	name := vethName("550e8400-e29b-41d4-a716-446655440000")
	if !strings.HasPrefix(name, "veth") {
		t.Errorf("vethName must start with 'veth', got %s", name)
	}
	if len(name) > 15 {
		t.Errorf("vethName exceeds Linux 15-char limit: len=%d name=%s", len(name), name)
	}

	n1 := vethName("aaa")
	n2 := vethName("bbb")
	if n1 == n2 {
		t.Errorf("different IDs produced the same veth name: %s", n1)
	}

	n3 := vethName("aaa")
	if n1 != n3 {
		t.Errorf("same ID produced different veth names: %s vs %s", n1, n3)
	}
}

func TestNormalizeOVNConnection(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"unix:/var/run/ovn/ovnnb_db.sock", "unix:/var/run/ovn/ovnnb_db.sock"},
		{"tcp:127.0.0.1:6641", "tcp:127.0.0.1:6641"},
		{"ssl:1.2.3.4:6641", "ssl:1.2.3.4:6641"},
		{"/var/run/ovn/ovnnb_db.sock", "unix:/var/run/ovn/ovnnb_db.sock"},
		{"127.0.0.1:6641", "tcp:127.0.0.1:6641"},
		{"somepath", "unix:somepath"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeConn(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeConn(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
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
			got := addressHasIP(tt.address, tt.ip)
			if got != tt.expected {
				t.Errorf("addressHasIP(%q, %q) = %v, want %v",
					tt.address, tt.ip, got, tt.expected)
			}
		})
	}
}

func TestMetaKey(t *testing.T) {
	key := metaKey("my-endpoint-id", "mac")
	expected := "docker:endpoint:my-endpoint-id:mac"
	if key != expected {
		t.Errorf("metaKey = %q, want %q", key, expected)
	}
}
