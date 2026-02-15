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

// OVS Database Models
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

// GetOVNNBConnection reads OVN NB connection from OVS database
func (o *OVSAPI) GetOVNNBConnection() (string, error) {
	// List all Open_vSwitch entries
	ovsList := []OpenvSwitch{}
	err := o.client.List(o.ctx, &ovsList)
	if err != nil {
		return "", fmt.Errorf("failed to list Open_vSwitch table: %w", err)
	}

	if len(ovsList) > 0 {
		openvSwitch := &ovsList[0]

		possibleKeys := []string{
			"ovn-nb",
		}

		for _, key := range possibleKeys {
			if nbConn, ok := openvSwitch.ExternalIDs[key]; ok && nbConn != "" {
				normalized := normalizeOVNConnection(nbConn)
				log.Printf("Found OVN NB connection: %s (key: %s, normalized: %s)", nbConn, key, normalized)
				return normalized, nil
			}
		}
	}

	defaultConnection := "unix:/var/run/ovn/ovnnb_db.sock"
	log.Printf("OVN NB connection not found in external_ids, using default: %s", defaultConnection)
	return defaultConnection, nil
}

// normalizeOVNConnection ensures the connection string has a proper scheme
func normalizeOVNConnection(conn string) string {
	if strings.HasPrefix(conn, "unix:") || strings.HasPrefix(conn, "tcp:") ||
		strings.HasPrefix(conn, "ssl:") || strings.HasPrefix(conn, "ptcp:") ||
		strings.HasPrefix(conn, "pssl:") {
		return conn
	}

	if strings.HasPrefix(conn, "/") {
		return "unix:" + conn
	}

	if strings.Contains(conn, ":") {
		return "tcp:" + conn
	}

	return "unix:" + conn
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

	ifaceUUID := fmt.Sprintf("iface_named_%s", interfaceName)
	portUUID := fmt.Sprintf("port_named_%s", ovsPortName)

	iface := &Interface{
		UUID: ifaceUUID,
		Name: interfaceName,
		Type: "",
		ExternalIDs: map[string]string{
			"iface-id": ifaceID,
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

	log.Printf("Successfully added port %s to OVS bridge %s with iface-id=%s", ovsPortName, bridgeName, ifaceID)
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
		log.Printf("Port %s not found in OVS, assuming already deleted", portName)
		return nil
	}

	port := &portList[0]

	bridge, found, err := o.findBridge(bridgeName)
	if err != nil {
		return err
	}
	if !found {
		log.Printf("Warning: bridge %s not found while removing port %s", bridgeName, portName)
	} else {
		bridgeMutateOps, err := o.client.Where(bridge).Mutate(bridge, model.Mutation{
			Field:   &bridge.Ports,
			Mutator: ovsdb.MutateOperationDelete,
			Value:   []string{port.UUID},
		})
		if err != nil {
			log.Printf("Warning: failed to create mutate operation for bridge: %v", err)
		} else {
			results, err := o.client.Transact(o.ctx, bridgeMutateOps...)
			if err != nil {
				log.Printf("Warning: failed to remove port from bridge: %v", err)
			} else if len(results) > 0 && results[0].Error != "" {
				log.Printf("Warning: failed to remove port from bridge: %s", results[0].Error)
			}
		}
	}

	portOps, err := o.client.Where(port).Delete()
	if err != nil {
		return fmt.Errorf("failed to create delete operation for port: %w", err)
	}

	results, err := o.client.Transact(o.ctx, portOps...)
	if err != nil {
		return fmt.Errorf("failed to remove port: %w", err)
	}

	if len(results) > 0 && results[0].Error != "" {
		return fmt.Errorf("failed to remove port: %s", results[0].Error)
	}

	log.Printf("Removed port %s from OVS", portName)

	ifaceList := []Interface{}
	err = o.client.WhereCache(func(i *Interface) bool {
		return i.Name == portName
	}).List(o.ctx, &ifaceList)
	if err == nil && len(ifaceList) > 0 {
		iface := &ifaceList[0]
		ifaceOps, err := o.client.Where(iface).Delete()
		if err != nil {
			log.Printf("Warning: failed to create delete operation for interface: %v", err)
		} else {
			results, err := o.client.Transact(o.ctx, ifaceOps...)
			if err != nil {
				log.Printf("Warning: failed to delete interface: %v", err)
			} else if len(results) > 0 && results[0].Error != "" {
				log.Printf("Warning: failed to delete interface: %s", results[0].Error)
			} else {
				log.Printf("Deleted interface %s from OVS", portName)
			}
		}
	}

	return nil
}
