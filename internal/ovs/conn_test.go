package ovs

import "testing"

func TestNormalizeConn(t *testing.T) {
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
			got := NormalizeConn(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeConn(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
