# Komari Telemetry Protocol v2

Canonical encoder and fixture source: `komari-agent/protocol/telemetryv2`.

- WebSocket subprotocol: `komari.telemetry.v2`
- Legacy fallback: `komari.telemetry.v1` or no selected subprotocol
- Byte order: little-endian
- Header: `KMR2`, version 2, flags, header size 16, exact payload size, schema ID `0x32525054`
- Maximum frame: 65,536 bytes
- Maximum message: 4,096 UTF-8 bytes
- Maximum GPU/model count: 64; maximum GPU/model name: 256 UTF-8 bytes

The fixed payload order is CPU, RAM, Swap, load1/load5/load15, Disk, network up/down/totalUp/totalDown, TCP/UDP, uptime, process, message and optional GPU. Detailed GPU entries contain name, total memory, used memory, utilization and temperature.

Unknown versions/flags/schema IDs, non-finite floats, invalid UTF-8, `used > total`, truncation, trailing data and oversized frames are rejected. A connection negotiated as v2 may send a JSON v1 text fallback when the Agent cannot safely represent a sample as v2.
