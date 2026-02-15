package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/go-logr/logr"
	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
)

// OVNDriver implements the Docker network driver interface
type OVNDriver struct {
	ovs       *OVSAPI
	ovn       *OVNAPI
	bridge    string
	ovsSocket string
}

// NetworkConfig stores network metadata
type NetworkConfig struct {
	ID      string
	Subnet  string
	Gateway string
	VLAN    int
}

// EndpointInfo stores endpoint metadata
type EndpointInfo struct {
	PortName    string
	MacAddr     string
	IPAddr      string
	VethHost    string
	OVSPortName string
}

// NewOVNDriver creates a new OVN driver instance
func NewOVNDriver(ovnBridge, ovsSocket string, ovsAPI *OVSAPI, ovnAPI *OVNAPI) *OVNDriver {
	return &OVNDriver{
		ovs:       ovsAPI,
		ovn:       ovnAPI,
		bridge:    ovnBridge,
		ovsSocket: ovsSocket,
	}
}

// GetCapabilities returns the driver's capabilities
func (d *OVNDriver) GetCapabilities() (*network.CapabilitiesResponse, error) {
	log.Println("GetCapabilities called")
	return &network.CapabilitiesResponse{
		Scope:             network.LocalScope,
		ConnectivityScope: network.GlobalScope,
	}, nil
}

// CreateNetwork creates a new OVN logical switch
func (d *OVNDriver) CreateNetwork(r *network.CreateNetworkRequest) error {
	log.Printf("CreateNetwork: %s", r.NetworkID)

	subnet := ""
	gateway := ""
	for _, ipam := range r.IPv4Data {
		subnet = ipam.Pool
		gateway = ipam.Gateway
	}

	if subnet == "" {
		return fmt.Errorf("subnet not specified")
	}

	if existingLS, found, err := d.ovn.GetLogicalSwitchBySubnet(subnet); err != nil {
		return err
	} else if found {
		return fmt.Errorf("subnet %s already in use by logical switch %s", subnet, existingLS.Name)
	}

	if gateway != "" && strings.Contains(gateway, "/") {
		ip, _, err := net.ParseCIDR(gateway)
		if err != nil {
			return fmt.Errorf("invalid gateway address: %w", err)
		}
		gateway = ip.String()
		log.Printf("Cleaned gateway from CIDR to IP: %s", gateway)
	}

	switchName := fmt.Sprintf("ls-%s", r.NetworkID[:12])

	otherConfig := map[string]string{
		"docker:network": r.NetworkID,
		"docker:subnet":  subnet,
		"docker:gateway": gateway,
	}

	if err := d.ovn.CreateLogicalSwitch(switchName, otherConfig); err != nil {
		return err
	}

	log.Printf("Created network %s with subnet %s, gateway %s", switchName, subnet, gateway)
	return nil
}

// DeleteNetwork removes an OVN logical switch
func (d *OVNDriver) DeleteNetwork(r *network.DeleteNetworkRequest) error {
	log.Printf("DeleteNetwork: %s", r.NetworkID)

	switchName := fmt.Sprintf("ls-%s", r.NetworkID[:12])

	return d.ovn.DeleteLogicalSwitch(switchName)
}

// CreateEndpoint creates a logical switch port for a container
func (d *OVNDriver) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	log.Printf("CreateEndpoint: %s on network %s", r.EndpointID, r.NetworkID)

	switchName := fmt.Sprintf("ls-%s", r.NetworkID[:12])
	if _, found, err := d.ovn.GetLogicalSwitch(switchName); err != nil || !found {
		return nil, fmt.Errorf("network %s not found", r.NetworkID)
	}

	macAddr := r.Interface.MacAddress
	if macAddr == "" {
		macAddr = generateMAC(r.EndpointID)
	}
	ipAddr := r.Interface.Address

	if strings.Contains(ipAddr, "/") {
		ip, _, err := net.ParseCIDR(ipAddr)
		if err != nil {
			return nil, fmt.Errorf("invalid IP address: %w", err)
		}
		ipAddr = ip.String()
	}

	if err := d.storeEndpointMetadata(switchName, r.EndpointID, macAddr, ipAddr); err != nil {
		return nil, err
	}

	log.Printf("Created endpoint %s with MAC %s, IP %s", r.EndpointID[:12], macAddr, ipAddr)
	return &network.CreateEndpointResponse{
		Interface: &network.EndpointInterface{
			MacAddress: macAddr,
		},
	}, nil
}

// DeleteEndpoint removes endpoint metadata
func (d *OVNDriver) DeleteEndpoint(r *network.DeleteEndpointRequest) error {
	log.Printf("DeleteEndpoint: %s", r.EndpointID)

	switchName := fmt.Sprintf("ls-%s", r.NetworkID[:12])
	return d.deleteEndpointMetadata(switchName, r.EndpointID)
}

// Join connects the endpoint to the network namespace
func (d *OVNDriver) Join(r *network.JoinRequest) (*network.JoinResponse, error) {
	log.Printf("Join: endpoint %s", r.EndpointID)

	switchName := fmt.Sprintf("ls-%s", r.NetworkID[:12])
	portName := fmt.Sprintf("lsp-%s-ls-%s", r.EndpointID[:12], r.NetworkID[:12])

	macAddr, ipAddr, gateway, err := d.getEndpointMetadata(switchName, r.EndpointID)
	if err != nil {
		return nil, err
	}

	addressStr := fmt.Sprintf("%s %s", macAddr, ipAddr)
	externalIDs := map[string]string{
		"docker:endpoint": r.EndpointID,
		"docker:network":  r.NetworkID,
	}

	if existingLSP, found, err := d.ovn.GetLogicalSwitchPortByIP(switchName, ipAddr); err != nil {
		return nil, err
	} else if found {
		return nil, fmt.Errorf("IP address %s already in use on logical switch %s by port %s", ipAddr, switchName, existingLSP.Name)
	}

	ls, found, err := d.ovn.GetLogicalSwitch(switchName)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("logical switch %s not found", switchName)
	}

	if _, found, err := d.ovn.GetLogicalSwitchPort(portName); err != nil {
		return nil, fmt.Errorf("failed to find logical switch port: %w", err)
	} else if found {
		return nil, fmt.Errorf("logical switch port %s already exists", portName)
	}

	enabled := true
	lsp := &LogicalSwitchPort{
		Name:         portName,
		Addresses:    []string{addressStr},
		PortSecurity: []string{addressStr},
		Enabled:      &enabled,
		Type:         "",
		ExternalIDs:  externalIDs,
	}

	cleanPortName := strings.ReplaceAll(portName, "-", "_")
	namedUUID := fmt.Sprintf("lsp_named_%s", cleanPortName)
	lsp.UUID = namedUUID

	lspOps, err := d.ovn.CreateLogicalSwitchPortOp(lsp)
	if err != nil {
		return nil, fmt.Errorf("failed to create logical switch port operation: %w", err)
	}

	mutateOps, err := d.ovn.MutateLogicalSwitchPortsOp(ls, ovsdb.MutateOperationInsert, []string{namedUUID})
	if err != nil {
		return nil, fmt.Errorf("failed to create mutate operation: %w", err)
	}

	allOps := append(lspOps, mutateOps...)
	results, err := d.ovn.Transact(allOps...)
	if err != nil {
		return nil, fmt.Errorf("failed to create logical switch port and attach to switch: %w", err)
	}

	for _, res := range results {
		if res.Error != "" {
			return nil, fmt.Errorf("transaction error: %s", res.Error)
		}
	}

	log.Printf("Created logical switch port %s with address %s", portName, addressStr)

	localVethName := fmt.Sprintf("veth%s", r.EndpointID[:7])
	containerVethName := localVethName + "_c"

	log.Printf("Creating veth pair: %s <-> %s", localVethName, containerVethName)
	cmd := exec.Command("ip", "link", "add", localVethName,
		"type", "veth", "peer", "name", containerVethName)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create veth pair: %w", err)
	}

	cmd = exec.Command("ip", "link", "set", containerVethName, "address", macAddr)
	if err := cmd.Run(); err != nil {
		exec.Command("ip", "link", "del", localVethName).Run()
		return nil, fmt.Errorf("failed to set MAC address: %w", err)
	}

	cmd = exec.Command("ip", "link", "set", localVethName, "up")
	if err := cmd.Run(); err != nil {
		exec.Command("ip", "link", "del", localVethName).Run()
		return nil, fmt.Errorf("failed to bring up host veth: %w", err)
	}

	ovsPortName := localVethName
	if err := d.ovs.AddPortToBridge(d.bridge, ovsPortName, localVethName, portName); err != nil {
		exec.Command("ip", "link", "del", localVethName).Run()
		return nil, fmt.Errorf("failed to add veth to OVS: %w", err)
	}

	exec.Command("ethtool", "-K", localVethName, "tx", "off").Run()
	exec.Command("ethtool", "-K", containerVethName, "tx", "off").Run()

	log.Printf("Join complete: returning gateway %s", gateway)
	return &network.JoinResponse{
		InterfaceName: network.InterfaceName{
			SrcName:   containerVethName,
			DstPrefix: "eth",
		},
		Gateway: gateway,
	}, nil
}

// Leave disconnects the endpoint
func (d *OVNDriver) Leave(r *network.LeaveRequest) error {
	log.Printf("Leave: endpoint %s", r.EndpointID)

	switchName := fmt.Sprintf("ls-%s", r.NetworkID[:12])
	portName := fmt.Sprintf("lsp-%s-ls-%s", r.EndpointID[:12], r.NetworkID[:12])
	if err := d.deleteLogicalSwitchPort(switchName, portName); err != nil {
		log.Printf("Warning: failed to delete LSP %s: %v", portName, err)
	}

	localVethName := fmt.Sprintf("veth%s", r.EndpointID[:7])
	if err := d.ovs.RemovePort(d.bridge, localVethName); err != nil {
		log.Printf("Warning: failed to remove OVS port from OVS: %v", err)
	}

	cmd := exec.Command("ip", "link", "del", localVethName)
	if err := cmd.Run(); err != nil {
		log.Printf("Warning: failed to delete veth pair: %v", err)
	}

	return nil
}

func endpointOtherConfigKey(endpointID string, suffix string) string {
	return fmt.Sprintf("docker:endpoint:%s:%s", endpointID, suffix)
}

func (d *OVNDriver) storeEndpointMetadata(lsName string, endpointID string, macAddr string, ipAddr string) error {
	ls, found, err := d.ovn.GetLogicalSwitch(lsName)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("logical switch %s not found", lsName)
	}

	macKey := endpointOtherConfigKey(endpointID, "mac")
	ipKey := endpointOtherConfigKey(endpointID, "ip")
	mutateOps, err := d.ovn.MutateLogicalSwitchOtherConfigOp(ls, ovsdb.MutateOperationInsert, map[string]string{
		macKey: macAddr,
		ipKey:  ipAddr,
	})
	if err != nil {
		return fmt.Errorf("failed to create mutate operation for endpoint metadata: %w", err)
	}

	results, err := d.ovn.Transact(mutateOps...)
	if err != nil {
		return fmt.Errorf("failed to store endpoint metadata: %w", err)
	}
	if len(results) > 0 && results[0].Error != "" {
		return fmt.Errorf("failed to store endpoint metadata: %s", results[0].Error)
	}

	return nil
}

func (d *OVNDriver) deleteEndpointMetadata(lsName string, endpointID string) error {
	ls, found, err := d.ovn.GetLogicalSwitch(lsName)
	if err != nil {
		return err
	}
	if !found {
		log.Printf("Warning: logical switch %s not found while deleting endpoint metadata", lsName)
		return nil
	}

	macKey := endpointOtherConfigKey(endpointID, "mac")
	ipKey := endpointOtherConfigKey(endpointID, "ip")
	mutateOps, err := d.ovn.MutateLogicalSwitchOtherConfigOp(ls, ovsdb.MutateOperationDelete, map[string]string{
		macKey: "",
		ipKey:  "",
	})
	if err != nil {
		log.Printf("Warning: failed to create mutate operation for endpoint metadata delete: %v", err)
		return nil
	}
	results, err := d.ovn.Transact(mutateOps...)
	if err != nil {
		log.Printf("Warning: failed to delete endpoint metadata: %v", err)
		return nil
	}
	if len(results) > 0 && results[0].Error != "" {
		log.Printf("Warning: failed to delete endpoint metadata: %s", results[0].Error)
		return nil
	}
	log.Printf("Deleted endpoint %s metadata", endpointID[:12])
	return nil
}

func (d *OVNDriver) getEndpointMetadata(lsName string, endpointID string) (string, string, string, error) {
	ls, found, err := d.ovn.GetLogicalSwitch(lsName)
	if err != nil {
		return "", "", "", err
	}
	if !found {
		return "", "", "", fmt.Errorf("logical switch %s not found", lsName)
	}

	macKey := endpointOtherConfigKey(endpointID, "mac")
	ipKey := endpointOtherConfigKey(endpointID, "ip")
	macAddr := ls.OtherConfig[macKey]
	ipAddr := ls.OtherConfig[ipKey]
	gateway := ls.OtherConfig["docker:gateway"]
	if macAddr == "" || ipAddr == "" {
		return "", "", "", fmt.Errorf("endpoint metadata not found in logical switch %s", lsName)
	}
	return macAddr, ipAddr, gateway, nil
}

func (d *OVNDriver) deleteLogicalSwitchPort(switchName string, portName string) error {
	lsp, found, err := d.ovn.GetLogicalSwitchPort(portName)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	ops := []ovsdb.Operation{}
	if ls, found, err := d.ovn.GetLogicalSwitch(switchName); err == nil && found {
		mutateOps, err := d.ovn.MutateLogicalSwitchPortsOp(ls, ovsdb.MutateOperationDelete, []string{lsp.UUID})
		if err != nil {
			log.Printf("Warning: failed to create mutate operation to remove port from switch: %v", err)
		} else {
			ops = append(ops, mutateOps...)
		}
	}

	lspOps, err := d.ovn.DeleteLogicalSwitchPortOp(lsp)
	if err != nil {
		return fmt.Errorf("failed to create delete operation for LSP: %w", err)
	}
	ops = append(ops, lspOps...)

	results, err := d.ovn.Transact(ops...)
	if err != nil {
		return err
	}
	for _, res := range results {
		if res.Error != "" {
			return fmt.Errorf("transaction error: %s", res.Error)
		}
	}

	log.Printf("Deleted logical switch port %s from OVN", portName)
	return nil
}

// ProgramExternalConnectivity sets up external connectivity
func (d *OVNDriver) ProgramExternalConnectivity(r *network.ProgramExternalConnectivityRequest) error {
	return nil
}

// RevokeExternalConnectivity removes external connectivity
func (d *OVNDriver) RevokeExternalConnectivity(r *network.RevokeExternalConnectivityRequest) error {
	return nil
}

// DiscoverNew is called on new node discovery
func (d *OVNDriver) DiscoverNew(r *network.DiscoveryNotification) error {
	return nil
}

// DiscoverDelete is called on node removal
func (d *OVNDriver) DiscoverDelete(r *network.DiscoveryNotification) error {
	return nil
}

// AllocateNetwork allocates network resources
func (d *OVNDriver) AllocateNetwork(r *network.AllocateNetworkRequest) (*network.AllocateNetworkResponse, error) {
	return &network.AllocateNetworkResponse{}, nil
}

// FreeNetwork frees network resources
func (d *OVNDriver) FreeNetwork(r *network.FreeNetworkRequest) error {
	return nil
}

// EndpointInfo returns endpoint information
func (d *OVNDriver) EndpointInfo(r *network.InfoRequest) (*network.InfoResponse, error) {
	return &network.InfoResponse{}, nil
}

// generateMAC creates a MAC address from endpoint ID
func generateMAC(endpointID string) string {
	mac := []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x00}
	for i := 0; i < 5 && i < len(endpointID); i++ {
		mac[i+1] = endpointID[i]
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}

func envOrDefault(key string, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func main() {
	bridge := envOrDefault("OVN_BRIDGE", "br-int")
	ovsSocket := envOrDefault("OVS_SOCKET", "unix:/var/run/openvswitch/db.sock")

	const DOCKER_PLUGIN_SOCKET = "/run/docker/plugins/ovn.sock"

	ctx := context.Background()

	ovsDBModel, err := model.NewClientDBModel("Open_vSwitch",
		map[string]model.Model{
			"Bridge":       &Bridge{},
			"Port":         &Port{},
			"Interface":    &Interface{},
			"Open_vSwitch": &OpenvSwitch{},
		})
	if err != nil {
		log.Fatalf("Failed to create OVS DB model: %v", err)
	}

	var discartLogger logr.Logger = logr.Discard()
	ovsClient, err := client.NewOVSDBClient(
		ovsDBModel,
		client.WithEndpoint(ovsSocket),
		client.WithLogger(&discartLogger),
	)
	if err != nil {
		log.Fatalf("Failed to create OVS client: %v", err)
	}

	if err := ovsClient.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect to OVS database: %v", err)
	}

	if _, err := ovsClient.Monitor(ctx,
		ovsClient.NewMonitor(
			client.WithTable(&Bridge{}),
			client.WithTable(&Port{}),
			client.WithTable(&Interface{}),
			client.WithTable(&OpenvSwitch{}),
		),
	); err != nil {
		log.Fatalf("Failed to monitor OVS database: %v", err)
	}

	ovsAPI := NewOVSAPI(ovsClient, ctx)

	ovnNBConn, err := ovsAPI.GetOVNNBConnection()
	if err != nil {
		log.Fatalf("Failed to get OVN NB connection: %v", err)
	}

	log.Printf("Using OVN NB connection: %s", ovnNBConn)

	ovnNBModel, err := model.NewClientDBModel("OVN_Northbound",
		map[string]model.Model{
			"Logical_Switch":      &LogicalSwitch{},
			"Logical_Switch_Port": &LogicalSwitchPort{},
		})
	if err != nil {
		log.Fatalf("Failed to create OVN NB DB model: %v", err)
	}

	ovnNBClient, err := client.NewOVSDBClient(ovnNBModel, client.WithEndpoint(ovnNBConn))
	if err != nil {
		log.Fatalf("Failed to create OVN NB client: %v", err)
	}

	if err := ovnNBClient.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect to OVN NB database: %v", err)
	}

	if _, err := ovnNBClient.Monitor(ctx,
		ovnNBClient.NewMonitor(
			client.WithTable(&LogicalSwitch{}),
			client.WithTable(&LogicalSwitchPort{}),
		),
	); err != nil {
		log.Fatalf("Failed to monitor OVN NB database: %v", err)
	}

	log.Println("Successfully connected to OVS and OVN databases")

	ovnAPI := NewOVNAPI(ovnNBClient, ctx)

	driver := NewOVNDriver(bridge, ovsSocket, ovsAPI, ovnAPI)

	pluginDir := filepath.Dir(DOCKER_PLUGIN_SOCKET)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		log.Fatalf("Failed to create plugin directory: %v", err)
	}

	os.Remove(DOCKER_PLUGIN_SOCKET)

	handler := network.NewHandler(driver)
	log.Printf("Starting OVN plugin on %s", DOCKER_PLUGIN_SOCKET)
	if err := handler.ServeUnix(DOCKER_PLUGIN_SOCKET, 0); err != nil {
		log.Fatalf("Failed to start plugin: %v", err)
	}
}
