package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

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

	if len(r.NetworkID) < 12 {
		return fmt.Errorf("network ID too short: %q", r.NetworkID)
	}

	subnet := ""
	gateway := ""
	for _, ipam := range r.IPv4Data {
		subnet = ipam.Pool
		gateway = ipam.Gateway
	}

	if subnet == "" {
		return fmt.Errorf("subnet not specified")
	}

	if _, _, err := net.ParseCIDR(subnet); err != nil {
		return fmt.Errorf("invalid subnet CIDR %q: %w", subnet, err)
	}

	if existingLS, found, err := d.ovn.GetSwitchBySubnet(subnet); err != nil {
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

	switchName := fmt.Sprintf("%s%s", SwitchNamePrefix, r.NetworkID[:12])

	otherConfig := map[string]string{
		KeyDockerNetwork: r.NetworkID,
		KeyDockerSubnet:  subnet,
		KeyDockerGateway: gateway,
	}

	if err := d.ovn.CreateSwitch(switchName, otherConfig); err != nil {
		return err
	}

	log.Printf("Created network %s with subnet %s, gateway %s", switchName, subnet, gateway)
	return nil
}

// DeleteNetwork removes an OVN logical switch
func (d *OVNDriver) DeleteNetwork(r *network.DeleteNetworkRequest) error {
	log.Printf("DeleteNetwork: %s", r.NetworkID)

	if len(r.NetworkID) < 12 {
		return fmt.Errorf("network ID too short: %q", r.NetworkID)
	}

	switchName := fmt.Sprintf("%s%s", SwitchNamePrefix, r.NetworkID[:12])

	return d.ovn.DeleteSwitch(switchName)
}

// CreateEndpoint creates a logical switch port for a container
func (d *OVNDriver) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	log.Printf("CreateEndpoint: %s on network %s", r.EndpointID, r.NetworkID)

	if len(r.NetworkID) < 12 {
		return nil, fmt.Errorf("network ID too short: %q", r.NetworkID)
	}
	if len(r.EndpointID) < 12 {
		return nil, fmt.Errorf("endpoint ID too short: %q", r.EndpointID)
	}

	switchName := fmt.Sprintf("%s%s", SwitchNamePrefix, r.NetworkID[:12])
	if _, found, err := d.ovn.GetSwitch(switchName); err != nil || !found {
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

	if err := d.storeMetadata(switchName, r.EndpointID, macAddr, ipAddr); err != nil {
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

	if len(r.NetworkID) < 12 {
		return fmt.Errorf("network ID too short: %q", r.NetworkID)
	}

	switchName := fmt.Sprintf("%s%s", SwitchNamePrefix, r.NetworkID[:12])
	return d.deleteMetadata(switchName, r.EndpointID)
}

// Join connects the endpoint to the network namespace
func (d *OVNDriver) Join(r *network.JoinRequest) (*network.JoinResponse, error) {
	log.Printf("Join: endpoint %s", r.EndpointID)

	if len(r.NetworkID) < 12 {
		return nil, fmt.Errorf("network ID too short: %q", r.NetworkID)
	}
	if len(r.EndpointID) < 12 {
		return nil, fmt.Errorf("endpoint ID too short: %q", r.EndpointID)
	}

	switchName := fmt.Sprintf("%s%s", SwitchNamePrefix, r.NetworkID[:12])
	portName := fmt.Sprintf("%s%s-%s%s", PortNamePrefix, r.EndpointID[:12], SwitchNamePrefix, r.NetworkID[:12])

	macAddr, ipAddr, gateway, err := d.loadMetadata(switchName, r.EndpointID)
	if err != nil {
		return nil, err
	}

	addressStr := fmt.Sprintf("%s %s", macAddr, ipAddr)
	externalIDs := map[string]string{
		KeyDockerEndpoint: r.EndpointID,
		KeyDockerNetwork:  r.NetworkID,
	}

	if existingLSP, found, err := d.ovn.GetPortByIP(switchName, ipAddr); err != nil {
		return nil, err
	} else if found {
		return nil, fmt.Errorf("IP address %s already in use on logical switch %s by port %s", ipAddr, switchName, existingLSP.Name)
	}

	ls, found, err := d.ovn.GetSwitch(switchName)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("logical switch %s not found", switchName)
	}

	if _, found, err := d.ovn.GetPort(portName); err != nil {
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
	namedUUID := fmt.Sprintf("%s%s", NamedUUIDPrefix, cleanPortName)
	lsp.UUID = namedUUID

	lspOps, err := d.ovn.CreatePortOp(lsp)
	if err != nil {
		return nil, fmt.Errorf("failed to create logical switch port operation: %w", err)
	}

	mutateOps, err := d.ovn.MutatePortsOp(ls, ovsdb.MutateOperationInsert, []string{namedUUID})
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

	localVethName := vethName(r.EndpointID)
	containerVethName := localVethName + ContainerVethSuffix

	log.Printf("Creating veth pair: %s <-> %s", localVethName, containerVethName)
	cmd := exec.Command("ip", "link", "add", localVethName, "type", "veth", "peer", "name", containerVethName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to create veth pair: %w: output: %s", err, strings.TrimSpace(string(out)))
	}

	cmd = exec.Command("ip", "link", "set", containerVethName, "address", macAddr)
	if err := cmd.Run(); err != nil {
		if cleanupErr := exec.Command("ip", "link", "del", localVethName).Run(); cleanupErr != nil {
			log.Printf("Warning: failed to clean up veth pair after MAC set failure: %v", cleanupErr)
		}
		return nil, fmt.Errorf("failed to set MAC address: %w", err)
	}

	cmd = exec.Command("ip", "link", "set", localVethName, "up")
	if err := cmd.Run(); err != nil {
		if err := exec.Command("ip", "link", "del", localVethName).Run(); err != nil {
			log.Printf("Warning: failed to clean up veth pair after local veth set up failure: %v", err)
		}
		return nil, fmt.Errorf("failed to bring up host veth: %w", err)
	}

	if err := d.ovs.AddPortToBridge(d.bridge, localVethName, localVethName, portName); err != nil {
		if cleanupErr := exec.Command("ip", "link", "del", localVethName).Run(); cleanupErr != nil {
			log.Printf("Warning: failed to clean up veth pair after OVS port add failure: %v", cleanupErr)
		}
		if rollbackErr := d.deletePort(switchName, portName); rollbackErr != nil {
			log.Printf("Warning: failed to roll back OVN LSP %s after OVS failure: %v", portName, rollbackErr)
		}
		return nil, fmt.Errorf("failed to add veth to OVS: %w", err)
	}

	if err := exec.Command("ethtool", "-K", localVethName, "tx", "off").Run(); err != nil {
		log.Printf("Warning: failed to disable tx offloading on host veth: %v", err)
	}
	if err := exec.Command("ethtool", "-K", containerVethName, "tx", "off").Run(); err != nil {
		log.Printf("Warning: failed to disable tx offloading on container veth: %v", err)
	}

	log.Printf("Join complete: returning gateway %s", gateway)
	return &network.JoinResponse{
		InterfaceName: network.InterfaceName{
			SrcName:   containerVethName,
			DstPrefix: DefaultDstPrefix,
		},
		Gateway: gateway,
	}, nil
}

// Leave disconnects the endpoint
func (d *OVNDriver) Leave(r *network.LeaveRequest) error {
	log.Printf("Leave: endpoint %s", r.EndpointID)

	if len(r.NetworkID) < 12 {
		return fmt.Errorf("network ID too short: %q", r.NetworkID)
	}
	if len(r.EndpointID) < 12 {
		return fmt.Errorf("endpoint ID too short: %q", r.EndpointID)
	}

	switchName := fmt.Sprintf("%s%s", SwitchNamePrefix, r.NetworkID[:12])
	portName := fmt.Sprintf("%s%s-%s%s", PortNamePrefix, r.EndpointID[:12], SwitchNamePrefix, r.NetworkID[:12])

	var errs []error

	if err := d.deletePort(switchName, portName); err != nil {
		log.Printf("Warning: failed to delete LSP %s: %v", portName, err)
		errs = append(errs, fmt.Errorf("delete LSP: %w", err))
	}

	localVethName := vethName(r.EndpointID)
	if err := d.ovs.RemovePort(d.bridge, localVethName); err != nil {
		log.Printf("Warning: failed to remove OVS port from OVS: %v", err)
		errs = append(errs, fmt.Errorf("remove OVS port: %w", err))
	}

	if err := exec.Command("ip", "link", "del", localVethName).Run(); err != nil {
		log.Printf("Warning: failed to delete veth pair: %v", err)
		errs = append(errs, fmt.Errorf("delete veth: %w", err))
	}

	return errors.Join(errs...)
}

func metaKey(endpointID string, suffix string) string {
	return fmt.Sprintf(MetaKeyFormat, endpointID, suffix)
}

func (d *OVNDriver) storeMetadata(switchName string, endpointID string, macAddr string, ipAddr string) error {
	ls, found, err := d.ovn.GetSwitch(switchName)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("logical switch %s not found", switchName)
	}

	macKey := metaKey(endpointID, MetaKeyMAC)
	ipKey := metaKey(endpointID, MetaKeyIP)
	mutateOps, err := d.ovn.MutateConfigOp(ls, ovsdb.MutateOperationInsert, map[string]string{
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

func (d *OVNDriver) deleteMetadata(switchName string, endpointID string) error {
	ls, found, err := d.ovn.GetSwitch(switchName)
	if err != nil {
		return err
	}
	if !found {
		log.Printf("Warning: logical switch %s not found while deleting endpoint metadata", switchName)
		return nil
	}

	macKey := metaKey(endpointID, MetaKeyMAC)
	ipKey := metaKey(endpointID, MetaKeyIP)
	mutateOps, err := d.ovn.MutateConfigOp(ls, ovsdb.MutateOperationDelete, map[string]string{
		macKey: "",
		ipKey:  "",
	})
	if err != nil {
		return fmt.Errorf("failed to create mutate operation for endpoint metadata delete: %w", err)
	}
	results, err := d.ovn.Transact(mutateOps...)
	if err != nil {
		return fmt.Errorf("failed to delete endpoint metadata: %w", err)
	}
	if len(results) > 0 && results[0].Error != "" {
		return fmt.Errorf("failed to delete endpoint metadata: %s", results[0].Error)
	}
	log.Printf("Deleted endpoint %s metadata", endpointID[:12])
	return nil
}

func (d *OVNDriver) loadMetadata(switchName string, endpointID string) (string, string, string, error) {
	ls, found, err := d.ovn.GetSwitch(switchName)
	if err != nil {
		return "", "", "", err
	}
	if !found {
		return "", "", "", fmt.Errorf("logical switch %s not found", switchName)
	}

	macKey := metaKey(endpointID, MetaKeyMAC)
	ipKey := metaKey(endpointID, MetaKeyIP)
	macAddr := ls.OtherConfig[macKey]
	ipAddr := ls.OtherConfig[ipKey]
	gateway := ls.OtherConfig[KeyDockerGateway]
	if macAddr == "" || ipAddr == "" {
		return "", "", "", fmt.Errorf("endpoint metadata not found in logical switch %s", switchName)
	}
	return macAddr, ipAddr, gateway, nil
}

func (d *OVNDriver) deletePort(switchName string, portName string) error {
	lsp, found, err := d.ovn.GetPort(portName)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	ops := []ovsdb.Operation{}
	if ls, found, err := d.ovn.GetSwitch(switchName); err == nil && found {
		mutateOps, err := d.ovn.MutatePortsOp(ls, ovsdb.MutateOperationDelete, []string{lsp.UUID})
		if err != nil {
			log.Printf("Warning: failed to create mutate operation to remove port from switch: %v", err)
		} else {
			ops = append(ops, mutateOps...)
		}
	}

	lspOps, err := d.ovn.DeletePortOp(lsp)
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

// generateMAC creates a locally-administered unicast MAC address from an
// endpoint ID using SHA-256 so the result is deterministic and unique.
func generateMAC(endpointID string) string {
	sum := sha256.Sum256([]byte(endpointID))
	// Set locally-administered (bit 1) and clear multicast (bit 0) in first octet.
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x",
		sum[0], sum[1], sum[2], sum[3], sum[4])
}

// vethName derives a deterministic, collision-resistant veth host-side name
// from the endpoint ID. Linux interface names are limited to 15 characters;
// "veth" (4) + 8 hex chars from SHA-256 = 12 chars. The container-side name is
// formed by appending "_c", so keeping the host veth to 12 chars ensures the
// container veth stays within the 15-char limit.
func vethName(endpointID string) string {
	sum := sha256.Sum256([]byte(endpointID))
	return fmt.Sprintf("%s%x", DefaultVethPrefix, sum[:4])
}

func envOrDefault(key string, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// DockerPluginSocket is the path where the plugin will listen for Docker API calls.
const DockerPluginSocket = "/run/docker/plugins/ovn.sock"

func main() {
	bridge := envOrDefault(EnvOVNBridge, DefaultOVNBridge)
	ovsSocket := envOrDefault(EnvOVSSocket, DefaultOVSSocket)

	ctx := context.Background()

	ovsDBModel, err := model.NewClientDBModel(DBOVS,
		map[string]model.Model{
			TableBridge:       &Bridge{},
			TablePort:         &Port{},
			TableInterface:    &Interface{},
			TableOpenvSwitch: &OpenvSwitch{},
		})
	if err != nil {
		log.Fatalf("Failed to create OVS DB model: %v", err)
	}

	discardLogger := logr.Discard()
	ovsClient, err := client.NewOVSDBClient(
		ovsDBModel,
		client.WithEndpoint(ovsSocket),
		client.WithLogger(&discardLogger),
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

	nbConn, err := ovsAPI.NBConnection()
	if err != nil {
		log.Fatalf("Failed to get OVN NB connection: %v", err)
	}

	log.Printf("Using OVN NB connection: %s", nbConn)

	nbModel, err := model.NewClientDBModel(DBOVNNB,
		map[string]model.Model{
			TableLogicalSwitch:      &LogicalSwitch{},
			TableLogicalSwitchPort: &LogicalSwitchPort{},
		})
	if err != nil {
		log.Fatalf("Failed to create OVN NB DB model: %v", err)
	}

	nbClient, err := client.NewOVSDBClient(nbModel, client.WithEndpoint(nbConn))
	if err != nil {
		log.Fatalf("Failed to create OVN NB client: %v", err)
	}

	if err := nbClient.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect to OVN NB database: %v", err)
	}

	if _, err := nbClient.Monitor(ctx,
		nbClient.NewMonitor(
			client.WithTable(&LogicalSwitch{}),
			client.WithTable(&LogicalSwitchPort{}),
		),
	); err != nil {
		log.Fatalf("Failed to monitor OVN NB database: %v", err)
	}

	log.Println("Successfully connected to OVS and OVN databases")

	defer ovsClient.Disconnect()
	defer nbClient.Disconnect()

	ovnAPI := NewOVNAPI(nbClient, ctx)

	driver := NewOVNDriver(bridge, ovsSocket, ovsAPI, ovnAPI)

	pluginDir := filepath.Dir(DockerPluginSocket)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		log.Fatalf("Failed to create plugin directory: %v", err)
	}

	if err := os.Remove(DockerPluginSocket); err != nil && !os.IsNotExist(err) {
		log.Fatalf("Failed to remove existing plugin socket: %v", err)
	}
	defer func() {
		if err := os.Remove(DockerPluginSocket); err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: failed to remove plugin socket during cleanup: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %s, shutting down", sig)
		if err := os.Remove(DockerPluginSocket); err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: failed to remove plugin socket during shutdown: %v", err)
		}
		ovsClient.Disconnect()
		nbClient.Disconnect()
		os.Exit(0)
	}()

	handler := network.NewHandler(driver)
	log.Printf("Starting OVN plugin on %s", DockerPluginSocket)
	if err := handler.ServeUnix(DockerPluginSocket, 0); err != nil {
		log.Fatalf("Failed to start plugin: %v", err)
	}
}
