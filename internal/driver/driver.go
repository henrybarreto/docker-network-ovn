package driver

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/ovn-org/libovsdb/ovsdb"

	"docker-network-ovn/internal/constants"
	"docker-network-ovn/internal/ipam"
	netutils "docker-network-ovn/internal/network"
	"docker-network-ovn/internal/ovn"
	"docker-network-ovn/internal/ovs"
)

// OVNDriver implements the Docker network driver interface
type OVNDriver struct {
	ovs       *ovs.OVSAPI
	ovn       *ovn.OVNAPI
	bridge    string
	ovsSocket string
}

// NewOVNDriver creates a new OVN driver instance
func NewOVNDriver(ovnBridge, ovsSocket string, ovsAPI *ovs.OVSAPI, ovnAPI *ovn.OVNAPI) *OVNDriver {
	return &OVNDriver{
		ovs:       ovsAPI,
		ovn:       ovnAPI,
		bridge:    ovnBridge,
		ovsSocket: ovsSocket,
	}
}

// GetCapabilities returns the driver's capabilities
func (d *OVNDriver) GetCapabilities() (*network.CapabilitiesResponse, error) {
	constants.Logger.Info("GetCapabilities called")
	return &network.CapabilitiesResponse{
		Scope:             network.LocalScope,
		ConnectivityScope: network.GlobalScope,
	}, nil
}

// CreateNetwork creates a new OVN logical switch
func (d *OVNDriver) CreateNetwork(r *network.CreateNetworkRequest) error {
	constants.Logger.Info("CreateNetwork", "network_id", r.NetworkID)

	if len(r.NetworkID) < 12 {
		return fmt.Errorf("network ID too short: %q", r.NetworkID)
	}

	subnet, gateway, err := ipam.ValidateIPv4Data(r.IPv4Data, r.IPv6Data)
	if err != nil {
		return err
	}

	if existingLS, found, err := d.ovn.GetSwitchBySubnet(subnet); err != nil {
		return err
	} else if found {
		return fmt.Errorf("subnet %s already in use by logical switch %s", subnet, existingLS.Name)
	}

	switchName := ovn.SwitchName(r.NetworkID)

	otherConfig := map[string]string{
		constants.KeyDockerNetwork: r.NetworkID,
		constants.KeyDockerSubnet:  subnet,
		constants.KeyDockerGateway: gateway,
	}

	if err := d.ovn.CreateSwitch(switchName, otherConfig); err != nil {
		return err
	}

	constants.Logger.Info("Created network", "switch", switchName, "subnet", subnet, "gateway", gateway)
	return nil
}

// DeleteNetwork removes an OVN logical switch
func (d *OVNDriver) DeleteNetwork(r *network.DeleteNetworkRequest) error {
	constants.Logger.Info("DeleteNetwork", "network_id", r.NetworkID)

	if len(r.NetworkID) < 12 {
		return fmt.Errorf("network ID too short: %q", r.NetworkID)
	}

	switchName := ovn.SwitchName(r.NetworkID)

	return d.ovn.DeleteSwitch(switchName)
}

// CreateEndpoint creates a logical switch port for a container
func (d *OVNDriver) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	constants.Logger.Info("CreateEndpoint", "endpoint_id", r.EndpointID, "network_id", r.NetworkID)

	if len(r.NetworkID) < 12 {
		return nil, fmt.Errorf("network ID too short: %q", r.NetworkID)
	}
	if len(r.EndpointID) < 12 {
		return nil, fmt.Errorf("endpoint ID too short: %q", r.EndpointID)
	}

	switchName := ovn.SwitchName(r.NetworkID)
	if _, found, err := d.ovn.GetSwitch(switchName); err != nil || !found {
		return nil, fmt.Errorf("network %s not found", r.NetworkID)
	}

	macAddr := r.Interface.MacAddress
	if macAddr == "" {
		macAddr = netutils.GenerateMAC(r.EndpointID)
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

	constants.Logger.Info("Created endpoint", "endpoint_id", r.EndpointID[:12], "mac", macAddr, "ip", ipAddr)
	return &network.CreateEndpointResponse{
		Interface: &network.EndpointInterface{
			MacAddress: macAddr,
		},
	}, nil
}

// DeleteEndpoint removes endpoint metadata
func (d *OVNDriver) DeleteEndpoint(r *network.DeleteEndpointRequest) error {
	constants.Logger.Info("DeleteEndpoint", "endpoint_id", r.EndpointID)

	if len(r.NetworkID) < 12 {
		return fmt.Errorf("network ID too short: %q", r.NetworkID)
	}

	switchName := ovn.SwitchName(r.NetworkID)
	return d.deleteMetadata(switchName, r.EndpointID)
}

// Join connects the endpoint to the network namespace
func (d *OVNDriver) Join(r *network.JoinRequest) (*network.JoinResponse, error) {
	constants.Logger.Info("Join", "endpoint_id", r.EndpointID, "network_id", r.NetworkID)

	if len(r.NetworkID) < 12 {
		return nil, fmt.Errorf("network ID too short: %q", r.NetworkID)
	}
	if len(r.EndpointID) < 12 {
		return nil, fmt.Errorf("endpoint ID too short: %q", r.EndpointID)
	}

	switchName := ovn.SwitchName(r.NetworkID)
	portName := ovn.PortName(r.EndpointID, r.NetworkID)

	macAddr, ipAddr, gateway, err := d.loadMetadata(switchName, r.EndpointID)
	if err != nil {
		return nil, err
	}

	addressStr := fmt.Sprintf("%s %s", macAddr, ipAddr)
	externalIDs := map[string]string{
		constants.KeyDockerEndpoint: r.EndpointID,
		constants.KeyDockerNetwork:  r.NetworkID,
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
	lsp := &ovn.LogicalSwitchPort{
		Name:         portName,
		Addresses:    []string{addressStr},
		PortSecurity: []string{addressStr},
		Enabled:      &enabled,
		Type:         "",
		ExternalIDs:  externalIDs,
	}

	cleanPortName := strings.ReplaceAll(portName, "-", "_")
	namedUUID := fmt.Sprintf("%s%s", constants.NamedUUIDPrefix, cleanPortName)
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

	constants.Logger.Info("Created logical switch port", "port", portName, "address", addressStr)

	localVethName := netutils.VethName(r.EndpointID)
	containerVethName := localVethName + netutils.ContainerVethSuffix

	constants.Logger.Info("Creating veth pair", "host_veth", localVethName, "container_veth", containerVethName)
	cmd := exec.Command("ip", "link", "add", localVethName, "type", "veth", "peer", "name", containerVethName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to create veth pair: %w: output: %s", err, strings.TrimSpace(string(out)))
	}

	cmd = exec.Command("ip", "link", "set", containerVethName, "address", macAddr)
	if err := cmd.Run(); err != nil {
		if cleanupErr := exec.Command("ip", "link", "del", localVethName).Run(); cleanupErr != nil {
			constants.Logger.Warn("Failed to clean up veth pair after MAC set failure", "err", cleanupErr)
		}
		return nil, fmt.Errorf("failed to set MAC address: %w", err)
	}

	cmd = exec.Command("ip", "link", "set", localVethName, "up")
	if err := cmd.Run(); err != nil {
		if err := exec.Command("ip", "link", "del", localVethName).Run(); err != nil {
			constants.Logger.Warn("Failed to clean up veth pair after local veth set up failure", "err", err)
		}
		return nil, fmt.Errorf("failed to bring up host veth: %w", err)
	}

	if err := d.ovs.AddPortToBridge(d.bridge, localVethName, localVethName, portName); err != nil {
		if cleanupErr := exec.Command("ip", "link", "del", localVethName).Run(); cleanupErr != nil {
			constants.Logger.Warn("Failed to clean up veth pair after OVS port add failure", "err", cleanupErr)
		}
		if rollbackErr := d.DeletePort(switchName, portName); rollbackErr != nil {
			constants.Logger.Warn("Failed to roll back OVN LSP after OVS failure", "port", portName, "err", rollbackErr)
		}
		return nil, fmt.Errorf("failed to add veth to OVS: %w", err)
	}

	if err := exec.Command("ethtool", "-K", localVethName, "tx", "off").Run(); err != nil {
		constants.Logger.Warn("Failed to disable tx offloading on host veth", "err", err)
	}
	if err := exec.Command("ethtool", "-K", containerVethName, "tx", "off").Run(); err != nil {
		constants.Logger.Warn("Failed to disable tx offloading on container veth", "err", err)
	}

	constants.Logger.Info("Join complete", "endpoint_id", r.EndpointID, "gateway", gateway)
	return &network.JoinResponse{
		InterfaceName: network.InterfaceName{
			SrcName:   containerVethName,
			DstPrefix: netutils.DefaultDstPrefix,
		},
		Gateway: gateway,
	}, nil
}

// Leave disconnects the endpoint
func (d *OVNDriver) Leave(r *network.LeaveRequest) error {
	constants.Logger.Info("Leave", "endpoint_id", r.EndpointID, "network_id", r.NetworkID)

	if len(r.NetworkID) < 12 {
		return fmt.Errorf("network ID too short: %q", r.NetworkID)
	}
	if len(r.EndpointID) < 12 {
		return fmt.Errorf("endpoint ID too short: %q", r.EndpointID)
	}

	switchName := ovn.SwitchName(r.NetworkID)
	portName := ovn.PortName(r.EndpointID, r.NetworkID)

	var errs []error

	if err := d.DeletePort(switchName, portName); err != nil {
		constants.Logger.Warn("Failed to delete LSP", "port", portName, "err", err)
		errs = append(errs, fmt.Errorf("delete LSP: %w", err))
	}

	localVethName := netutils.VethName(r.EndpointID)
	if err := d.ovs.RemovePort(d.bridge, localVethName); err != nil {
		constants.Logger.Warn("Failed to remove OVS port from OVS", "port", localVethName, "err", err)
		errs = append(errs, fmt.Errorf("remove OVS port: %w", err))
	}

	if err := exec.Command("ip", "link", "del", localVethName).Run(); err != nil {
		constants.Logger.Warn("Failed to delete veth pair", "veth", localVethName, "err", err)
		errs = append(errs, fmt.Errorf("delete veth: %w", err))
	}

	return errors.Join(errs...)
}

func (d *OVNDriver) storeMetadata(switchName string, endpointID string, macAddr string, ipAddr string) error {
	ls, found, err := d.ovn.GetSwitch(switchName)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("logical switch %s not found", switchName)
	}

	macKey := ovn.MetaKey(endpointID, ovn.MetaKeyMAC)
	ipKey := ovn.MetaKey(endpointID, ovn.MetaKeyIP)
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
		constants.Logger.Warn("Logical switch not found while deleting endpoint metadata", "switch", switchName)
		return nil
	}

	macKey := ovn.MetaKey(endpointID, ovn.MetaKeyMAC)
	ipKey := ovn.MetaKey(endpointID, ovn.MetaKeyIP)
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
	constants.Logger.Info("Deleted endpoint metadata", "endpoint_id", endpointID[:12])
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

	macKey := ovn.MetaKey(endpointID, ovn.MetaKeyMAC)
	ipKey := ovn.MetaKey(endpointID, ovn.MetaKeyIP)
	macAddr := ls.OtherConfig[macKey]
	ipAddr := ls.OtherConfig[ipKey]
	gateway := ls.OtherConfig[constants.KeyDockerGateway]
	if macAddr == "" || ipAddr == "" {
		return "", "", "", fmt.Errorf("endpoint metadata not found in logical switch %s", switchName)
	}
	return macAddr, ipAddr, gateway, nil
}

func (d *OVNDriver) DeletePort(switchName string, portName string) error {
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
			constants.Logger.Warn("Failed to create mutate operation to remove port from switch", "switch", switchName, "port", portName, "err", err)
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

	constants.Logger.Info("Deleted logical switch port from OVN", "port", portName)
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
	constants.Logger.Info("DiscoverNew", "notification", r)
	return nil
}

// DiscoverDelete is called on node removal
func (d *OVNDriver) DiscoverDelete(r *network.DiscoveryNotification) error {
	constants.Logger.Info("DiscoverDelete", "notification", r)
	return nil
}

// AllocateNetwork allocates network resources
func (d *OVNDriver) AllocateNetwork(r *network.AllocateNetworkRequest) (*network.AllocateNetworkResponse, error) {
	ipv4 := make([]*network.IPAMData, len(r.IPv4Data))
	for i := range r.IPv4Data {
		ipv4[i] = &r.IPv4Data[i]
	}
	ipv6 := make([]*network.IPAMData, len(r.IPv6Data))
	for i := range r.IPv6Data {
		ipv6[i] = &r.IPv6Data[i]
	}
	_, _, err := ipam.ValidateIPv4Data(ipv4, ipv6)
	if err != nil {
		return nil, err
	}
	return &network.AllocateNetworkResponse{}, nil
}

// FreeNetwork frees network resources
func (d *OVNDriver) FreeNetwork(r *network.FreeNetworkRequest) error {
	constants.Logger.Info("FreeNetwork", "network_id", r.NetworkID)
	return nil
}

// EndpointInfo returns endpoint information
func (d *OVNDriver) EndpointInfo(r *network.InfoRequest) (*network.InfoResponse, error) {
	if len(r.NetworkID) < 12 {
		return nil, fmt.Errorf("network ID too short: %q", r.NetworkID)
	}
	if len(r.EndpointID) < 12 {
		return nil, fmt.Errorf("endpoint ID too short: %q", r.EndpointID)
	}

	sw := ovn.SwitchName(r.NetworkID)
	mac, ip, gateway, err := d.loadMetadata(sw, r.EndpointID)
	if err != nil {
		return nil, err
	}

	port := ovn.PortName(r.EndpointID, r.NetworkID)
	return &network.InfoResponse{
		Value: map[string]string{
			"mac":      mac,
			"ip":       ip,
			"gateway":  gateway,
			"port":     port,
			"iface-id": port,
		},
	}, nil
}

// Reconcile removes stale OVN LSPs and OVS ports that no longer have matching metadata.
func (d *OVNDriver) Reconcile() error {
	switches, err := d.ovn.ListSwitches()
	if err != nil {
		return err
	}
	ports, err := d.ovn.ListPorts()
	if err != nil {
		return err
	}

	portByUUID := map[string]ovn.LogicalSwitchPort{}
	for _, p := range ports {
		portByUUID[p.UUID] = p
	}

	for _, ls := range switches {
		for _, uuid := range ls.Ports {
			p, ok := portByUUID[uuid]
			if !ok {
				continue
			}
			endpointID := p.ExternalIDs[constants.KeyDockerEndpoint]
			if endpointID == "" {
				continue
			}
			macKey := ovn.MetaKey(endpointID, ovn.MetaKeyMAC)
			ipKey := ovn.MetaKey(endpointID, ovn.MetaKeyIP)
			if ls.OtherConfig[macKey] == "" || ls.OtherConfig[ipKey] == "" {
				constants.Logger.Warn("Removing OVN port missing metadata", "port", p.Name, "switch", ls.Name, "endpoint_id", endpointID)
				if err := d.DeletePort(ls.Name, p.Name); err != nil {
					return err
				}
			}
		}
	}

	ifaces, err := d.ovs.ListInterfaces()
	if err != nil {
		return err
	}
	ifaceByName := map[string]ovs.Interface{}
	for _, iface := range ifaces {
		ifaceByName[iface.Name] = iface
	}

	ovsPorts, err := d.ovs.ListPorts()
	if err != nil {
		return err
	}
	for _, port := range ovsPorts {
		iface, ok := ifaceByName[port.Name]
		if !ok {
			continue
		}
		ifaceID := iface.ExternalIDs[constants.KeyIfaceID]
		if ifaceID == "" {
			continue
		}
		if _, found, err := d.ovn.GetPort(ifaceID); err != nil {
			return err
		} else if !found {
			constants.Logger.Warn("Removing OVS port without OVN LSP", "port", port.Name, "iface_id", ifaceID)
			if err := d.ovs.RemovePort(d.bridge, port.Name); err != nil {
				return err
			}
		}
	}

	return nil
}
