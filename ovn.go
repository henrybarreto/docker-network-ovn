package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
)

// OVN Northbound Database Models
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

func (o *OVNAPI) findLogicalSwitch(name string) (*LogicalSwitch, bool, error) {
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

func (o *OVNAPI) findLogicalSwitchPort(name string) (*LogicalSwitchPort, bool, error) {
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

func (o *OVNAPI) findLogicalSwitchBySubnet(subnet string) (*LogicalSwitch, bool, error) {
	list := []LogicalSwitch{}
	err := o.client.WhereCache(func(ls *LogicalSwitch) bool {
		return ls.OtherConfig != nil && ls.OtherConfig["docker:subnet"] == subnet
	}).List(o.ctx, &list)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list logical switches by subnet: %w", err)
	}
	if len(list) == 0 {
		return nil, false, nil
	}
	return &list[0], true, nil
}

func (o *OVNAPI) findLogicalSwitchPortByIP(switchName string, ipAddr string) (*LogicalSwitchPort, bool, error) {
	ls, found, err := o.findLogicalSwitch(switchName)
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
			if logicalSwitchPortAddressHasIP(addr, ipAddr) {
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

func logicalSwitchPortAddressHasIP(address string, ipAddr string) bool {
	if address == ipAddr {
		return true
	}
	parts := strings.Fields(address)
	for _, part := range parts {
		if part == ipAddr {
			return true
		}
	}
	return false
}

// GetLogicalSwitch returns a logical switch by name
func (o *OVNAPI) GetLogicalSwitch(name string) (*LogicalSwitch, bool, error) {
	return o.findLogicalSwitch(name)
}

// GetLogicalSwitchPort returns a logical switch port by name
func (o *OVNAPI) GetLogicalSwitchPort(name string) (*LogicalSwitchPort, bool, error) {
	return o.findLogicalSwitchPort(name)
}

// GetLogicalSwitchBySubnet returns a logical switch with matching docker:subnet
func (o *OVNAPI) GetLogicalSwitchBySubnet(subnet string) (*LogicalSwitch, bool, error) {
	return o.findLogicalSwitchBySubnet(subnet)
}

// GetLogicalSwitchPortByIP returns a logical switch port on a switch matching an IP
func (o *OVNAPI) GetLogicalSwitchPortByIP(switchName string, ipAddr string) (*LogicalSwitchPort, bool, error) {
	return o.findLogicalSwitchPortByIP(switchName, ipAddr)
}

// Transact executes a set of OVN Northbound operations
func (o *OVNAPI) Transact(ops ...ovsdb.Operation) ([]ovsdb.OperationResult, error) {
	return o.client.Transact(o.ctx, ops...)
}

// CreateLogicalSwitch creates a logical switch
func (o *OVNAPI) CreateLogicalSwitch(name string, otherConfig map[string]string) error {
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

// DeleteLogicalSwitch deletes a logical switch if it exists
func (o *OVNAPI) DeleteLogicalSwitch(name string) error {
	ls, found, err := o.findLogicalSwitch(name)
	if err != nil {
		return err
	}
	if !found {
		log.Printf("Logical switch %s not found, assuming already deleted", name)
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

	log.Printf("Deleted logical switch %s", name)
	return nil
}

// MutateLogicalSwitchOtherConfigOp builds a mutation operation on a switch other_config
func (o *OVNAPI) MutateLogicalSwitchOtherConfigOp(ls *LogicalSwitch, mutator ovsdb.Mutator, values map[string]string) ([]ovsdb.Operation, error) {
	return o.client.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.OtherConfig,
		Mutator: mutator,
		Value:   values,
	})
}

// CreateLogicalSwitchPortOp builds an operation to create a logical switch port
func (o *OVNAPI) CreateLogicalSwitchPortOp(lsp *LogicalSwitchPort) ([]ovsdb.Operation, error) {
	return o.client.Create(lsp)
}

// DeleteLogicalSwitchPortOp builds an operation to delete a logical switch port
func (o *OVNAPI) DeleteLogicalSwitchPortOp(lsp *LogicalSwitchPort) ([]ovsdb.Operation, error) {
	return o.client.Where(lsp).Delete()
}

// MutateLogicalSwitchPortsOp builds a mutation operation on a switch ports list
func (o *OVNAPI) MutateLogicalSwitchPortsOp(ls *LogicalSwitch, mutator ovsdb.Mutator, portUUIDs []string) ([]ovsdb.Operation, error) {
	return o.client.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: mutator,
		Value:   portUUIDs,
	})
}
