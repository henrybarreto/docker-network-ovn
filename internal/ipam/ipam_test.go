package ipam

import (
	"testing"

	"github.com/docker/go-plugins-helpers/network"
)

func TestValidateIPv4Data(t *testing.T) {
	valid := []*network.IPAMData{{Pool: "10.0.0.0/24", Gateway: "10.0.0.1"}}
	if subnet, gw, err := ValidateIPv4Data(valid, nil); err != nil || subnet != "10.0.0.0/24" || gw != "10.0.0.1" {
		t.Fatalf("unexpected result: subnet=%s gw=%s err=%v", subnet, gw, err)
	}

	_, _, err := ValidateIPv4Data(nil, nil)
	if err == nil {
		t.Fatalf("expected error for missing subnet")
	}

	_, _, err = ValidateIPv4Data([]*network.IPAMData{{Pool: "10.0.0.0/24"}, {Pool: "10.1.0.0/24"}}, nil)
	if err == nil {
		t.Fatalf("expected error for multiple subnets")
	}

	_, _, err = ValidateIPv4Data([]*network.IPAMData{{Pool: "bad-cidr"}}, nil)
	if err == nil {
		t.Fatalf("expected error for bad cidr")
	}

	_, _, err = ValidateIPv4Data([]*network.IPAMData{{Pool: "10.0.0.0/24"}}, []*network.IPAMData{{Pool: "fd00::/64"}})
	if err == nil {
		t.Fatalf("expected error for ipv6 unsupported")
	}
}

func TestNormalizeGateway(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"", "", true},
		{"10.0.0.1", "10.0.0.1", true},
		{"10.0.0.1/24", "10.0.0.1", true},
		{"not-an-ip", "", false},
	}
	for _, tt := range tests {
		got, err := NormalizeGateway(tt.input)
		if tt.ok && err != nil {
			t.Fatalf("expected success for %q, got err %v", tt.input, err)
		}
		if !tt.ok && err == nil {
			t.Fatalf("expected error for %q", tt.input)
		}
		if tt.ok && got != tt.want {
			t.Fatalf("normalizeGateway(%q)=%q want %q", tt.input, got, tt.want)
		}
	}
}
