package telemetryv2

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Encode exists on the server so the v3 codec and cross-repository goldens use
// exactly the same nested v2 representation as the Agent.
func Encode(report Report) ([]byte, error) {
	if err := validateReport(report); err != nil {
		return nil, err
	}
	frame := make([]byte, HeaderSize, 256)
	frame = appendFloat64(frame, report.CPUUsage)
	frame = appendMemory(frame, report.RAM)
	frame = appendMemory(frame, report.Swap)
	frame = appendFloat64(frame, report.Load.Load1)
	frame = appendFloat64(frame, report.Load.Load5)
	frame = appendFloat64(frame, report.Load.Load15)
	frame = appendMemory(frame, report.Disk)
	frame = binary.LittleEndian.AppendUint64(frame, report.Network.Up)
	frame = binary.LittleEndian.AppendUint64(frame, report.Network.Down)
	frame = binary.LittleEndian.AppendUint64(frame, report.Network.TotalUp)
	frame = binary.LittleEndian.AppendUint64(frame, report.Network.TotalDown)
	frame = binary.LittleEndian.AppendUint32(frame, report.Connections.TCP)
	frame = binary.LittleEndian.AppendUint32(frame, report.Connections.UDP)
	frame = binary.LittleEndian.AppendUint64(frame, report.Uptime)
	frame = binary.LittleEndian.AppendUint32(frame, report.Process)
	frame = appendString(frame, report.Message)
	flags := uint8(0)
	if report.GPU != nil {
		flags |= flagGPU
		if report.GPU.Detailed {
			flags |= flagGPUDetailed
			frame = binary.LittleEndian.AppendUint16(frame, uint16(len(report.GPU.Devices)))
			frame = appendFloat64(frame, report.GPU.AverageUsage)
			for _, device := range report.GPU.Devices {
				frame = appendString(frame, device.Name)
				frame = binary.LittleEndian.AppendUint64(frame, device.MemoryTotal)
				frame = binary.LittleEndian.AppendUint64(frame, device.MemoryUsed)
				frame = appendFloat64(frame, device.Utilization)
				frame = binary.LittleEndian.AppendUint64(frame, device.Temperature)
			}
		} else {
			frame = binary.LittleEndian.AppendUint16(frame, uint16(len(report.GPU.Models)))
			for _, model := range report.GPU.Models {
				frame = appendString(frame, model)
			}
		}
	}
	if len(frame) > MaxFrameSize {
		return nil, fmt.Errorf("telemetry v2 frame is %d bytes, maximum is %d", len(frame), MaxFrameSize)
	}
	copy(frame[:4], frameMagic[:])
	frame[4], frame[5] = Version, flags
	binary.LittleEndian.PutUint16(frame[6:8], HeaderSize)
	binary.LittleEndian.PutUint32(frame[8:12], uint32(len(frame)-HeaderSize))
	binary.LittleEndian.PutUint32(frame[12:16], SchemaID)
	return frame, nil
}

func appendMemory(destination []byte, memory Memory) []byte {
	destination = binary.LittleEndian.AppendUint64(destination, memory.Total)
	return binary.LittleEndian.AppendUint64(destination, memory.Used)
}

func appendFloat64(destination []byte, value float64) []byte {
	return binary.LittleEndian.AppendUint64(destination, math.Float64bits(value))
}

func appendString(destination []byte, value string) []byte {
	destination = binary.LittleEndian.AppendUint16(destination, uint16(len(value)))
	return append(destination, value...)
}
