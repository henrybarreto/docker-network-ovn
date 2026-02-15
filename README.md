# docker-network-ovn

Docker network plugin that provisions OVN logical switches and ports, wiring container veth pairs into OVS.

**Version:** 0.1.0

## Features
- Creates OVN logical switches per Docker network.
- Creates OVN logical switch ports per endpoint with IP/MAC tracking.
- Wires container veth pairs into OVS with `iface-id` set to the OVN LSP.
- Uses OVSDB/OVN NB database connections discovered from OVS.

## Requirements
- Linux host with OVS and OVN running.
- Docker with plugin support.
- OVSDB socket accessible at `/var/run/openvswitch/db.sock` (or custom).
- OVN NB socket available via OVS external IDs or default `/var/run/ovn/ovnnb_db.sock`.

## Configuration
Environment variables:
- `OVN_BRIDGE` (default: `br-int`)
- `OVS_SOCKET` (default: `unix:/var/run/openvswitch/db.sock`)

## Build
```bash
go build -o docker-network-ovn
```

## Run (development)
```bash
sudo ./docker-network-ovn
```

The plugin listens on `/run/docker/plugins/ovn.sock`.

## Example

Create the network
```bash
docker network create -d ovn --subnet 172.16.0.0/16 --gateway 172.16.0.1 ovn0
```

Create container with the network
```
docker run --rm -it --net=ovn0 alpine /bin/sh
```

## Notes
- This is an early 0.1.0 release; expect breaking changes.
- External connectivity hooks are stubbed for now.
