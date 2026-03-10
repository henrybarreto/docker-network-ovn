package ovn

import (
	"context"
	"fmt"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"

	"docker-network-ovn/internal/constants"
	"docker-network-ovn/internal/network"
)

type LogicalSwitch struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Ports       []string          `ovsdb:"ports"`
	OtherConfig map[string]string `ovsdb:"other_config"`
}

type LogicalSwitchPort struct {
	UUID         string            `ovsdb:"_uuid"`
	Name         string            `ovsdb:"name"`
	Addresses    []string          `ovsdb:"addresses"`
	PortSecurity []string          `ovsdb:"port_security"`
	Enabled      *bool             `ovsdb:"enabled"`
	Type         string            `ovsdb:"type"`
	Options      map[string]string `ovsdb:"options"`
	ExternalIDs  map[string]string `ovsdb:"external_ids"`
}

// OVNAPI provides a clean abstraction for OVN Northbound operations
type OVNAPI struct {
	client client.Client
	ctx    context.Context
}

func NewOVNAPI(c client.Client, ctx context.Context) *OVNAPI {
	return &OVNAPI{client: c, ctx: ctx}
}

func (o *OVNAPI) ListSwitches() ([]LogicalSwitch, error) {
	list := []LogicalSwitch{}
	if err := o.client.WhereCache(func(_ *LogicalSwitch) bool { return true }).List(o.ctx, &list); err != nil {
		return nil, fmt.Errorf("failed to list logical switches: %w", err)
	}
	return list, nil
}

func (o *OVNAPI) ListPorts() ([]LogicalSwitchPort, error) {
	list := []LogicalSwitchPort{}
	if err := o.client.WhereCache(func(_ *LogicalSwitchPort) bool { return true }).List(o.ctx, &list); err != nil {
		return nil, fmt.Errorf("failed to list logical switch ports: %w", err)
	}
	return list, nil
}

// GetSwitch returns a logical switch by name
func (o *OVNAPI) GetSwitch(name string) (*LogicalSwitch, bool, error) {
	list := []LogicalSwitch{}
	err := o.client.WhereCache(func(ls *LogicalSwitch) bool {
		return ls.Name == name
	}).List(o.ctx, &list)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list logical switches: %w", err)
	}
	if len(list) == 0 {
		return nil, false, nil
	}
	return &list[0], true, nil
}

// GetPort returns a logical switch port by name
func (o *OVNAPI) GetPort(name string) (*LogicalSwitchPort, bool, error) {
	list := []LogicalSwitchPort{}
	err := o.client.WhereCache(func(lsp *LogicalSwitchPort) bool {
		return lsp.Name == name
	}).List(o.ctx, &list)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list logical switch ports: %w", err)
	}
	if len(list) == 0 {
		return nil, false, nil
	}
	return &list[0], true, nil
}

// GetSwitchBySubnet returns a logical switch with matching docker:subnet
func (o *OVNAPI) GetSwitchBySubnet(subnet string) (*LogicalSwitch, bool, error) {
	list := []LogicalSwitch{}
	err := o.client.WhereCache(func(ls *LogicalSwitch) bool {
		return ls.OtherConfig != nil && ls.OtherConfig[constants.KeyDockerSubnet] == subnet
	}).List(o.ctx, &list)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list logical switches by subnet: %w", err)
	}
	if len(list) == 0 {
		return nil, false, nil
	}
	return &list[0], true, nil
}

// GetPortByIP returns a logical switch port on a switch matching an IP
func (o *OVNAPI) GetPortByIP(switchName string, ipAddr string) (*LogicalSwitchPort, bool, error) {
	ls, found, err := o.GetSwitch(switchName)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	portUUIDs := map[string]struct{}{}
	for _, uuid := range ls.Ports {
		portUUIDs[uuid] = struct{}{}
	}

	list := []LogicalSwitchPort{}
	err = o.client.WhereCache(func(lsp *LogicalSwitchPort) bool {
		if _, ok := portUUIDs[lsp.UUID]; !ok {
			return false
		}
		for _, addr := range lsp.Addresses {
			if network.AddressHasIP(addr, ipAddr) {
				return true
			}
		}
		return false
	}).List(o.ctx, &list)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list logical switch ports by IP: %w", err)
	}
	if len(list) == 0 {
		return nil, false, nil
	}
	return &list[0], true, nil
}

// Transact executes a set of OVN Northbound operations
func (o *OVNAPI) Transact(ops ...ovsdb.Operation) ([]ovsdb.OperationResult, error) {
	return o.client.Transact(o.ctx, ops...)
}

// CreateSwitch creates a logical switch
func (o *OVNAPI) CreateSwitch(name string, otherConfig map[string]string) error {
	ls := &LogicalSwitch{
		Name:        name,
		OtherConfig: otherConfig,
	}

	ops, err := o.client.Create(ls)
	if err != nil {
		return fmt.Errorf("failed to create logical switch operation: %w", err)
	}

	results, err := o.client.Transact(o.ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to create logical switch: %w", err)
	}

	if len(results) == 0 || results[0].Error != "" {
		errMsg := "unknown error"
		if len(results) > 0 {
			errMsg = results[0].Error
		}
		return fmt.Errorf("failed to create logical switch: %s", errMsg)
	}

	return nil
}

// DeleteSwitch deletes a logical switch if it exists
func (o *OVNAPI) DeleteSwitch(name string) error {
	ls, found, err := o.GetSwitch(name)
	if err != nil {
		return err
	}
	if !found {
		constants.Logger.Info("Logical switch already deleted", "switch", name)
		return nil
	}

	ops, err := o.client.Where(ls).Delete()
	if err != nil {
		return fmt.Errorf("failed to create delete operation: %w", err)
	}

	results, err := o.client.Transact(o.ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to delete logical switch: %w", err)
	}

	if len(results) > 0 && results[0].Error != "" {
		return fmt.Errorf("failed to delete logical switch: %s", results[0].Error)
	}

	constants.Logger.Info("Deleted logical switch", "switch", name)
	return nil
}

// MutateConfigOp builds a mutation operation on a switch other_config
func (o *OVNAPI) MutateConfigOp(ls *LogicalSwitch, mutator ovsdb.Mutator, values map[string]string) ([]ovsdb.Operation, error) {
	return o.client.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.OtherConfig,
		Mutator: mutator,
		Value:   values,
	})
}

// CreatePortOp builds an operation to create a logical switch port
func (o *OVNAPI) CreatePortOp(lsp *LogicalSwitchPort) ([]ovsdb.Operation, error) {
	return o.client.Create(lsp)
}

// DeletePortOp builds an operation to delete a logical switch port
func (o *OVNAPI) DeletePortOp(lsp *LogicalSwitchPort) ([]ovsdb.Operation, error) {
	return o.client.Where(lsp).Delete()
}

// MutatePortsOp builds a mutation operation on a switch ports list
func (o *OVNAPI) MutatePortsOp(ls *LogicalSwitch, mutator ovsdb.Mutator, portUUIDs []string) ([]ovsdb.Operation, error) {
	return o.client.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: mutator,
		Value:   portUUIDs,
	})
}
