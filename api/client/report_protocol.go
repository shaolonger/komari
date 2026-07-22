package client

import (
	"fmt"
	"math"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/protocol/telemetryv2"
)

type telemetryProtocol uint8

const (
	telemetryProtocolV1 telemetryProtocol = iota + 1
	telemetryProtocolV2
)

func negotiatedTelemetryProtocol(selected string) (telemetryProtocol, error) {
	switch selected {
	case "", telemetryv2.LegacySubprotocol:
		return telemetryProtocolV1, nil
	case telemetryv2.Subprotocol:
		return telemetryProtocolV2, nil
	default:
		return telemetryProtocolV1, fmt.Errorf("unsupported telemetry subprotocol %q", selected)
	}
}

func decodeTelemetryV2Report(frame []byte) (common.Report, error) {
	decoded, err := telemetryv2.Decode(frame)
	if err != nil {
		return common.Report{}, err
	}
	return telemetryV2ToCommon(decoded)
}

func telemetryV2ToCommon(decoded telemetryv2.Report) (common.Report, error) {
	ramTotal, err := telemetryUint64ToInt64("RAM total", decoded.RAM.Total)
	if err != nil {
		return common.Report{}, err
	}
	ramUsed, err := telemetryUint64ToInt64("RAM used", decoded.RAM.Used)
	if err != nil {
		return common.Report{}, err
	}
	swapTotal, err := telemetryUint64ToInt64("swap total", decoded.Swap.Total)
	if err != nil {
		return common.Report{}, err
	}
	swapUsed, err := telemetryUint64ToInt64("swap used", decoded.Swap.Used)
	if err != nil {
		return common.Report{}, err
	}
	diskTotal, err := telemetryUint64ToInt64("disk total", decoded.Disk.Total)
	if err != nil {
		return common.Report{}, err
	}
	diskUsed, err := telemetryUint64ToInt64("disk used", decoded.Disk.Used)
	if err != nil {
		return common.Report{}, err
	}
	networkUp, err := telemetryUint64ToInt64("network up", decoded.Network.Up)
	if err != nil {
		return common.Report{}, err
	}
	networkDown, err := telemetryUint64ToInt64("network down", decoded.Network.Down)
	if err != nil {
		return common.Report{}, err
	}
	networkTotalUp, err := telemetryUint64ToInt64("network total up", decoded.Network.TotalUp)
	if err != nil {
		return common.Report{}, err
	}
	networkTotalDown, err := telemetryUint64ToInt64("network total down", decoded.Network.TotalDown)
	if err != nil {
		return common.Report{}, err
	}
	uptime, err := telemetryUint64ToInt64("uptime", decoded.Uptime)
	if err != nil {
		return common.Report{}, err
	}
	tcp, err := telemetryUint32ToInt("TCP connections", decoded.Connections.TCP)
	if err != nil {
		return common.Report{}, err
	}
	udp, err := telemetryUint32ToInt("UDP connections", decoded.Connections.UDP)
	if err != nil {
		return common.Report{}, err
	}
	process, err := telemetryUint32ToInt("process count", decoded.Process)
	if err != nil {
		return common.Report{}, err
	}

	report := common.Report{
		CPU:         common.CPUReport{Usage: decoded.CPUUsage},
		Ram:         common.RamReport{Total: ramTotal, Used: ramUsed},
		Swap:        common.RamReport{Total: swapTotal, Used: swapUsed},
		Load:        common.LoadReport{Load1: decoded.Load.Load1, Load5: decoded.Load.Load5, Load15: decoded.Load.Load15},
		Disk:        common.DiskReport{Total: diskTotal, Used: diskUsed},
		Network:     common.NetworkReport{Up: networkUp, Down: networkDown, TotalUp: networkTotalUp, TotalDown: networkTotalDown},
		Connections: common.ConnectionsReport{TCP: tcp, UDP: udp},
		Uptime:      uptime,
		Process:     process,
		Message:     decoded.Message,
	}
	if decoded.GPU == nil || !decoded.GPU.Detailed {
		return report, nil
	}
	gpu := &common.GPUDetailReport{
		Count:        len(decoded.GPU.Devices),
		AverageUsage: decoded.GPU.AverageUsage,
		DetailedInfo: make([]common.GPUDeviceInfo, len(decoded.GPU.Devices)),
	}
	for index, device := range decoded.GPU.Devices {
		memoryTotal, err := telemetryUint64ToInt64("GPU memory total", device.MemoryTotal)
		if err != nil {
			return common.Report{}, err
		}
		memoryUsed, err := telemetryUint64ToInt64("GPU memory used", device.MemoryUsed)
		if err != nil {
			return common.Report{}, err
		}
		temperature, err := telemetryUint64ToInt("GPU temperature", device.Temperature)
		if err != nil {
			return common.Report{}, err
		}
		gpu.DetailedInfo[index] = common.GPUDeviceInfo{
			Name:        device.Name,
			MemoryTotal: memoryTotal,
			MemoryUsed:  memoryUsed,
			Utilization: device.Utilization,
			Temperature: temperature,
		}
	}
	report.GPU = gpu
	return report, nil
}

func telemetryUint64ToInt64(name string, value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("%s exceeds int64", name)
	}
	return int64(value), nil
}

func telemetryUint32ToInt(name string, value uint32) (int, error) {
	return telemetryUint64ToInt(name, uint64(value))
}

func telemetryUint64ToInt(name string, value uint64) (int, error) {
	maximum := uint64(^uint(0) >> 1)
	if value > maximum {
		return 0, fmt.Errorf("%s exceeds int", name)
	}
	return int(value), nil
}
