# docker-network-ovn

Docker network plugin that provisions OVN logical switches and ports, wiring container veth pairs into OVS.

**Version:** 0.1.1

## Features
- Creates OVN logical switches per Docker network.
- Creates OVN logical switch ports per endpoint with IP/MAC tracking.
- Wires container veth pairs into OVS with `iface-id` set to the OVN LSP.
- Uses OVSDB/OVN NB database connections discovered from OVS.

## Requirements
- Linux host with OVS, OVN Host and a connection to OVN Central.
- Docker CE with plugin support.
- OVSDB socket accessible at `/var/run/openvswitch/db.sock` (or custom).
- OVN NB socket available via OVS external IDs or default `/var/run/ovn/ovnnb_db.sock`.


## Configuration

Create or edit `/etc/default/docker-network-ovn`:

```
OVN_BRIDGE=br-int
OVS_SOCKET=unix:/var/run/openvswitch/db.sock
```

## Development

```bash
sudo go run .
```

The plugin listens on `/run/docker/plugins/ovn.sock`.

## Packages

## Debian/Ubuntu

The repository has the instructions to build `.deb` package, but it is worth to
notice that the debian package depends on official [Docker CE](https://docs.docker.com/engine/install/debian/) ones.

Install build depenedencies:

```bash
sudo apt build-dep .
```

Build the pacakge:

```bash
debuild -us -uc
```

Install the pacakge:

```bash
sudo apt install ../docker-network-ovn_0.1.1-1_amd64.deb
```

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
- This is an early 0.1.1 release; expect breaking changes.
- External connectivity hooks are stubbed for now.
