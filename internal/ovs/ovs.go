package ovs

import (
	"context"
	"fmt"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"

	"docker-network-ovn/internal/constants"
)

type Bridge struct {
	UUID  string   `ovsdb:"_uuid"`
	Name  string   `ovsdb:"name"`
	Ports []string `ovsdb:"ports"`
}

type Port struct {
	UUID       string   `ovsdb:"_uuid"`
	Name       string   `ovsdb:"name"`
	Interfaces []string `ovsdb:"interfaces"`
}

type Interface struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Type        string            `ovsdb:"type"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

type OpenvSwitch struct {
	UUID        string            `ovsdb:"_uuid"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

// OVSAPI provides a clean abstraction for OVS operations
type OVSAPI struct {
	client client.Client
	ctx    context.Context
}

func NewOVSAPI(c client.Client, ctx context.Context) *OVSAPI {
	return &OVSAPI{client: c, ctx: ctx}
}

func (o *OVSAPI) ListPorts() ([]Port, error) {
	ports := []Port{}
	if err := o.client.WhereCache(func(_ *Port) bool { return true }).List(o.ctx, &ports); err != nil {
		return nil, fmt.Errorf("failed to list ports: %w", err)
	}
	return ports, nil
}

func (o *OVSAPI) ListInterfaces() ([]Interface, error) {
	ifaces := []Interface{}
	if err := o.client.WhereCache(func(_ *Interface) bool { return true }).List(o.ctx, &ifaces); err != nil {
		return nil, fmt.Errorf("failed to list interfaces: %w", err)
	}
	return ifaces, nil
}

// NBConnection reads OVN NB connection from OVS database
func (o *OVSAPI) NBConnection() (string, error) {
	ovsList := []OpenvSwitch{}
	err := o.client.WhereCache(func(_ *OpenvSwitch) bool { return true }).List(o.ctx, &ovsList)
	if err != nil {
		return "", fmt.Errorf("failed to list %s table: %w", constants.TableOpenvSwitch, err)
	}

	if len(ovsList) > 0 {
		openvSwitch := &ovsList[0]

		// Try common keys used by different OVN deployment tools.
		for _, key := range []string{constants.KeyOVNNB, constants.KeyOVNRemote} {
			if nbConn, ok := openvSwitch.ExternalIDs[key]; ok && nbConn != "" {
				normalized := NormalizeConn(nbConn)
				constants.Logger.Info("Found OVN NB connection", "raw", nbConn, "key", key, "normalized", normalized)
				return normalized, nil
			}
		}
	}

	defaultConnection := constants.DefaultOVNNBSocket
	constants.Logger.Info("OVN NB connection not found in external_ids; using default", "conn", defaultConnection)
	return defaultConnection, nil
}

func (o *OVSAPI) findBridge(name string) (*Bridge, bool, error) {
	bridgeList := []Bridge{}
	err := o.client.WhereCache(func(b *Bridge) bool {
		return b.Name == name
	}).List(o.ctx, &bridgeList)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list bridges: %w", err)
	}
	if len(bridgeList) == 0 {
		return nil, false, nil
	}
	return &bridgeList[0], true, nil
}

// AddPortToBridge adds a port and interface to an OVS bridge
func (o *OVSAPI) AddPortToBridge(bridgeName string, ovsPortName string, interfaceName string, ifaceID string) error {
	bridge, found, err := o.findBridge(bridgeName)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("bridge %s not found", bridgeName)
	}

	ifaceUUID := fmt.Sprintf("%s%s", constants.NamedIfacePrefix, interfaceName)
	portUUID := fmt.Sprintf("%s%s", constants.NamedPortPrefix, ovsPortName)

	iface := &Interface{
		UUID: ifaceUUID,
		Name: interfaceName,
		Type: "",
		ExternalIDs: map[string]string{
			constants.KeyIfaceID: ifaceID,
		},
	}

	port := &Port{
		UUID:       portUUID,
		Name:       ovsPortName,
		Interfaces: []string{ifaceUUID},
	}

	ifaceOps, err := o.client.Create(iface)
	if err != nil {
		return fmt.Errorf("failed to create interface operation: %w", err)
	}

	portOps, err := o.client.Create(port)
	if err != nil {
		return fmt.Errorf("failed to create port operation: %w", err)
	}

	bridgeMutateOps, err := o.client.Where(bridge).Mutate(bridge, model.Mutation{
		Field:   &bridge.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{portUUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create mutate operation for bridge: %w", err)
	}

	allOps := append(ifaceOps, portOps...)
	allOps = append(allOps, bridgeMutateOps...)
	results, err := o.client.Transact(o.ctx, allOps...)
	if err != nil {
		return fmt.Errorf("failed to create interface/port and attach to bridge: %w", err)
	}

	for _, res := range results {
		if res.Error != "" {
			return fmt.Errorf("transaction error: %s", res.Error)
		}
	}

	constants.Logger.Info("Added port to OVS bridge", "port", ovsPortName, "bridge", bridgeName, "iface_id", ifaceID)
	return nil
}

// RemovePort removes a port from an OVS bridge and deletes its interface
func (o *OVSAPI) RemovePort(bridgeName string, portName string) error {
	portList := []Port{}
	err := o.client.WhereCache(func(p *Port) bool {
		return p.Name == portName
	}).List(o.ctx, &portList)
	if err != nil {
		return fmt.Errorf("failed to list ports: %w", err)
	}
	if len(portList) == 0 {
		constants.Logger.Info("Port not found in OVS; assuming deleted", "port", portName)
		return nil
	}

	port := &portList[0]

	// Build all operations for a single atomic transaction: remove the port UUID
	// from the bridge, delete the port record, and delete the interface record.
	var allOps []ovsdb.Operation

	bridge, found, err := o.findBridge(bridgeName)
	if err != nil {
		return err
	}
	if found {
		bridgeMutateOps, err := o.client.Where(bridge).Mutate(bridge, model.Mutation{
			Field:   &bridge.Ports,
			Mutator: ovsdb.MutateOperationDelete,
			Value:   []string{port.UUID},
		})
		if err != nil {
			return fmt.Errorf("failed to create bridge mutate operation: %w", err)
		}
		allOps = append(allOps, bridgeMutateOps...)
	} else {
		constants.Logger.Warn("Bridge not found while removing port", "bridge", bridgeName, "port", portName)
	}

	portOps, err := o.client.Where(port).Delete()
	if err != nil {
		return fmt.Errorf("failed to create port delete operation: %w", err)
	}
	allOps = append(allOps, portOps...)

	ifaceList := []Interface{}
	if err = o.client.WhereCache(func(i *Interface) bool {
		return i.Name == portName
	}).List(o.ctx, &ifaceList); err == nil && len(ifaceList) > 0 {
		ifaceOps, err := o.client.Where(&ifaceList[0]).Delete()
		if err != nil {
			constants.Logger.Warn("Failed to create interface delete operation", "err", err)
		} else {
			allOps = append(allOps, ifaceOps...)
		}
	}

	results, err := o.client.Transact(o.ctx, allOps...)
	if err != nil {
		return fmt.Errorf("failed to remove port %s: %w", portName, err)
	}
	for _, res := range results {
		if res.Error != "" {
			return fmt.Errorf("transaction error removing port %s: %s", portName, res.Error)
		}
	}

	constants.Logger.Info("Removed port from OVS bridge", "port", portName, "bridge", bridgeName)
	return nil
}
