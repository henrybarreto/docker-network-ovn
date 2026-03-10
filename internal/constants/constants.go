package constants

import (
	"log/slog"
	"os"
)

var Logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

func SetLogger(l *slog.Logger) {
	Logger = l
}

const (
	// Default values
	DefaultOVNBridge   = "br-int"
	DefaultOVSSocket   = "unix:/var/run/openvswitch/db.sock"
	DefaultOVNNBSocket = "unix:/var/run/ovn/ovnnb_db.sock"

	// Resource name prefixes
	NamedUUIDPrefix  = "lsp_named_"
	NamedIfacePrefix = "iface_named_"
	NamedPortPrefix  = "port_named_"

	// Docker-specific OVN and OVS configuration keys
	KeyDockerNetwork  = "docker:network"
	KeyDockerSubnet   = "docker:subnet"
	KeyDockerGateway  = "docker:gateway"
	KeyDockerEndpoint = "docker:endpoint"
	KeyIfaceID        = "iface-id"

	// OVSDB Database and Table names
	DBOVS                  = "Open_vSwitch"
	DBOVNNB                = "OVN_Northbound"
	TableBridge            = "Bridge"
	TablePort              = "Port"
	TableInterface         = "Interface"
	TableOpenvSwitch       = "Open_vSwitch"
	TableLogicalSwitch     = "Logical_Switch"
	TableLogicalSwitchPort = "Logical_Switch_Port"

	// OVN Northbound connection keys in OVS
	KeyOVNNB     = "ovn-nb"
	KeyOVNRemote = "ovn-remote"
)
