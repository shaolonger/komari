// Package telemetry owns bounded, sharded real-time snapshots and minute
// accumulators. It contains no database or transport concerns.
package telemetry

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
)

const (
	ShardCount          = 256
	MaxSamplesPerMinute = 120
	MaxGPUDevices       = 64
	SnapshotTTL         = time.Minute
	topFraction         = 0.3
	topCapacity         = int(MaxSamplesPerMinute * topFraction)
)

var (
	ErrSampleLimit       = fmt.Errorf("telemetry sample limit of %d per minute exceeded", MaxSamplesPerMinute)
	ErrTooManyGPUDevices = fmt.Errorf("telemetry GPU device limit of %d exceeded", MaxGPUDevices)
	ErrWindowBacklog     = errors.New("telemetry accumulator has two undrained minute windows")
)

type Store struct {
	shards [ShardCount]shard
	now    func() time.Time
}

type shard struct {
	mu    sync.RWMutex
	nodes map[string]*nodeState
}

type nodeState struct {
	latest        common.Report
	latestAt      time.Time
	currentStart  time.Time
	current       *accumulator
	previousStart time.Time
	previous      *accumulator
}

type Aggregate struct {
	WindowStart time.Time
	WindowEnd   time.Time
	Record      models.Record
	GPURecords  []models.GPURecord
}

func NewStore() *Store {
	store := &Store{now: time.Now}
	for i := range store.shards {
		store.shards[i].nodes = make(map[string]*nodeState)
	}
	return store
}

func (s *Store) Add(uuid string, report common.Report) error {
	if uuid == "" {
		return errors.New("telemetry UUID is required")
	}
	if len(gpuDevices(report)) > MaxGPUDevices {
		return ErrTooManyGPUDevices
	}
	if report.UpdatedAt.IsZero() {
		report.UpdatedAt = s.now()
	}
	report.UUID = uuid
	window := report.UpdatedAt.Truncate(time.Minute)
	sh := s.shard(uuid)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	state := sh.nodes[uuid]
	if state == nil {
		state = &nodeState{}
		sh.nodes[uuid] = state
	}
	if state.latestAt.IsZero() || !report.UpdatedAt.Before(state.latestAt) {
		copyReport(&state.latest, report)
		state.latestAt = report.UpdatedAt
	}

	target, err := state.accumulatorFor(window)
	if err != nil {
		return err
	}
	return target.add(report)
}

func (state *nodeState) accumulatorFor(window time.Time) (*accumulator, error) {
	if state.current == nil {
		state.currentStart = window
		state.current = newAccumulator()
		return state.current, nil
	}
	if window.Equal(state.currentStart) {
		return state.current, nil
	}
	if state.previous != nil && window.Equal(state.previousStart) {
		return state.previous, nil
	}
	if window.After(state.currentStart) {
		if state.previous != nil {
			return nil, ErrWindowBacklog
		}
		state.previous, state.previousStart = state.current, state.currentStart
		state.current, state.currentStart = newAccumulator(), window
		return state.current, nil
	}
	if state.previous == nil {
		state.previous, state.previousStart = newAccumulator(), window
		return state.previous, nil
	}
	return nil, ErrWindowBacklog
}

func (s *Store) Latest(uuid string) (common.Report, bool) {
	sh := s.shard(uuid)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	state := sh.nodes[uuid]
	if state == nil || state.latestAt.IsZero() || s.now().Sub(state.latestAt) > SnapshotTTL {
		return common.Report{}, false
	}
	return cloneReport(state.latest), true
}

// Recent preserves the public API's array shape while exposing the immutable
// latest snapshot rather than retaining an unbounded slice of raw reports.
func (s *Store) Recent(uuid string) []common.Report {
	report, ok := s.Latest(uuid)
	if !ok {
		return []common.Report{}
	}
	return []common.Report{report}
}

// DrainBefore atomically detaches complete minute windows. New Add calls can
// proceed as soon as each shard has been visited; aggregation happens outside locks.
func (s *Store) DrainBefore(cutoff time.Time) []Aggregate {
	boundary := cutoff.Truncate(time.Minute)
	type detached struct {
		uuid  string
		start time.Time
		acc   *accumulator
	}
	var pending []detached
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for uuid, state := range sh.nodes {
			if state.previous != nil && state.previousStart.Before(boundary) {
				pending = append(pending, detached{uuid: uuid, start: state.previousStart, acc: state.previous})
				state.previous = nil
				state.previousStart = time.Time{}
			}
			if state.current != nil && state.currentStart.Before(boundary) {
				pending = append(pending, detached{uuid: uuid, start: state.currentStart, acc: state.current})
				state.current = nil
				state.currentStart = time.Time{}
			}
			if state.current == nil && state.previous == nil && cutoff.Sub(state.latestAt) > SnapshotTTL {
				delete(sh.nodes, uuid)
			}
		}
		sh.mu.Unlock()
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].start.Equal(pending[j].start) {
			return pending[i].uuid < pending[j].uuid
		}
		return pending[i].start.Before(pending[j].start)
	})
	result := make([]Aggregate, 0, len(pending))
	for _, item := range pending {
		persistedAt := item.start.Add(time.Minute)
		record, gpu := item.acc.records(item.uuid, persistedAt)
		result = append(result, Aggregate{WindowStart: item.start, WindowEnd: persistedAt, Record: record, GPURecords: gpu})
	}
	return result
}

func (s *Store) NodeCount() int {
	total := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		total += len(sh.nodes)
		sh.mu.RUnlock()
	}
	return total
}

func (s *Store) shard(uuid string) *shard {
	const offset32 = uint32(2166136261)
	const prime32 = uint32(16777619)
	hash := offset32
	for i := 0; i < len(uuid); i++ {
		hash ^= uint32(uuid[i])
		hash *= prime32
	}
	return &s.shards[hash&(ShardCount-1)]
}

func cloneReport(report common.Report) common.Report {
	clone := report
	if report.GPU != nil {
		gpu := *report.GPU
		gpu.DetailedInfo = slices.Clone(report.GPU.DetailedInfo)
		clone.GPU = &gpu
	}
	return clone
}

func copyReport(destination *common.Report, source common.Report) {
	if source.GPU == nil {
		*destination = source
		return
	}
	ownedGPU := destination.GPU
	if ownedGPU == nil {
		ownedGPU = &common.GPUDetailReport{}
	}
	ownedDetails := ownedGPU.DetailedInfo
	*ownedGPU = *source.GPU
	ownedGPU.DetailedInfo = append(ownedDetails[:0], source.GPU.DetailedInfo...)
	*destination = source
	destination.GPU = ownedGPU
}

func gpuDevices(report common.Report) []common.GPUDeviceInfo {
	if report.GPU == nil {
		return nil
	}
	return report.GPU.DetailedInfo
}

type floatTop struct {
	values [topCapacity]float64
	len    int
}

func (h *floatTop) add(value float64) {
	if h.len < len(h.values) {
		h.values[h.len] = value
		h.len++
		h.up(h.len - 1)
		return
	}
	if value <= h.values[0] {
		return
	}
	h.values[0] = value
	h.down(0)
}

func (h *floatTop) average(count int) float64 {
	want := topCount(count)
	values := h.values[:h.len]
	slices.Sort(values)
	start := len(values) - want
	var sum float64
	for _, value := range values[start:] {
		sum += value
	}
	return sum / float64(want)
}

func (h *floatTop) up(index int) {
	for index > 0 {
		parent := (index - 1) / 2
		if h.values[parent] <= h.values[index] {
			return
		}
		h.values[parent], h.values[index] = h.values[index], h.values[parent]
		index = parent
	}
}

func (h *floatTop) down(index int) {
	for {
		left := index*2 + 1
		if left >= h.len {
			return
		}
		smallest := left
		right := left + 1
		if right < h.len && h.values[right] < h.values[left] {
			smallest = right
		}
		if h.values[index] <= h.values[smallest] {
			return
		}
		h.values[index], h.values[smallest] = h.values[smallest], h.values[index]
		index = smallest
	}
}

type intTop struct {
	values [topCapacity]int64
	len    int
}

func (h *intTop) add(value int64) {
	if h.len < len(h.values) {
		h.values[h.len] = value
		h.len++
		h.up(h.len - 1)
		return
	}
	if value <= h.values[0] {
		return
	}
	h.values[0] = value
	h.down(0)
}

func (h *intTop) average(count int) int64 {
	want := topCount(count)
	values := h.values[:h.len]
	slices.Sort(values)
	start := len(values) - want
	var sum int64
	for _, value := range values[start:] {
		sum += value
	}
	return sum / int64(want)
}

func (h *intTop) up(index int) {
	for index > 0 {
		parent := (index - 1) / 2
		if h.values[parent] <= h.values[index] {
			return
		}
		h.values[parent], h.values[index] = h.values[index], h.values[parent]
		index = parent
	}
}

func (h *intTop) down(index int) {
	for {
		left := index*2 + 1
		if left >= h.len {
			return
		}
		smallest := left
		right := left + 1
		if right < h.len && h.values[right] < h.values[left] {
			smallest = right
		}
		if h.values[index] <= h.values[smallest] {
			return
		}
		h.values[index], h.values[smallest] = h.values[smallest], h.values[index]
		index = smallest
	}
}

func topCount(count int) int {
	want := int(float64(count) * topFraction)
	if want < 1 {
		want = 1
	}
	if want > topCapacity {
		want = topCapacity
	}
	return want
}

type accumulator struct {
	count                          int
	cpu, gpu, load                 floatTop
	ram, swap, disk                intTop
	netIn, netOut                  intTop
	netTotalUp, netTotalDown       intTop
	process, tcp, udp              intTop
	ramTotal, swapTotal, diskTotal int64
	devices                        map[int]*gpuAccumulator
}

type gpuAccumulator struct {
	count       int
	name        string
	memTotal    intTop
	memUsed     intTop
	utilization floatTop
	temperature intTop
}

func newAccumulator() *accumulator { return &accumulator{} }

func (a *accumulator) add(report common.Report) error {
	if a.count >= MaxSamplesPerMinute {
		return ErrSampleLimit
	}
	if len(gpuDevices(report)) > MaxGPUDevices {
		return ErrTooManyGPUDevices
	}
	a.count++
	a.cpu.add(report.CPU.Usage)
	a.load.add(report.Load.Load1)
	gpuAverage := 0.0
	if report.GPU != nil {
		gpuAverage = report.GPU.AverageUsage
	}
	a.gpu.add(gpuAverage)
	a.ram.add(report.Ram.Used)
	a.swap.add(report.Swap.Used)
	a.disk.add(report.Disk.Used)
	a.netIn.add(report.Network.Down)
	a.netOut.add(report.Network.Up)
	a.netTotalUp.add(report.Network.TotalUp)
	a.netTotalDown.add(report.Network.TotalDown)
	a.process.add(int64(report.Process))
	a.tcp.add(int64(report.Connections.TCP))
	a.udp.add(int64(report.Connections.UDP))
	a.ramTotal, a.swapTotal, a.diskTotal = report.Ram.Total, report.Swap.Total, report.Disk.Total
	if report.GPU != nil {
		if len(report.GPU.DetailedInfo) > 0 && a.devices == nil {
			a.devices = make(map[int]*gpuAccumulator, len(report.GPU.DetailedInfo))
		}
		for index, device := range report.GPU.DetailedInfo {
			gpu := a.devices[index]
			if gpu == nil {
				gpu = &gpuAccumulator{name: device.Name}
				a.devices[index] = gpu
			}
			gpu.count++
			gpu.name = device.Name
			gpu.memTotal.add(device.MemoryTotal)
			gpu.memUsed.add(device.MemoryUsed)
			gpu.utilization.add(device.Utilization)
			gpu.temperature.add(int64(device.Temperature))
		}
	}
	return nil
}

func (a *accumulator) records(uuid string, at time.Time) (models.Record, []models.GPURecord) {
	record := models.Record{
		Client: uuid, Time: models.FromTime(at),
		Cpu: float32(a.cpu.average(a.count)), Gpu: float32(a.gpu.average(a.count)),
		Ram: a.ram.average(a.count), RamTotal: a.ramTotal,
		Swap: a.swap.average(a.count), SwapTotal: a.swapTotal,
		Load: float32(a.load.average(a.count)),
		Disk: a.disk.average(a.count), DiskTotal: a.diskTotal,
		NetIn: a.netIn.average(a.count), NetOut: a.netOut.average(a.count),
		NetTotalUp: a.netTotalUp.average(a.count), NetTotalDown: a.netTotalDown.average(a.count),
		Process: int(a.process.average(a.count)), Connections: int(a.tcp.average(a.count)), ConnectionsUdp: int(a.udp.average(a.count)),
	}
	indices := make([]int, 0, len(a.devices))
	for index := range a.devices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	gpuRecords := make([]models.GPURecord, 0, len(indices))
	for _, index := range indices {
		gpu := a.devices[index]
		gpuRecords = append(gpuRecords, models.GPURecord{
			Client: uuid, Time: models.FromTime(at), DeviceIndex: index, DeviceName: gpu.name,
			MemTotal: gpu.memTotal.average(gpu.count), MemUsed: gpu.memUsed.average(gpu.count),
			Utilization: float32(gpu.utilization.average(gpu.count)), Temperature: int(gpu.temperature.average(gpu.count)),
		})
	}
	return record, gpuRecords
}
