// Package telemetry owns bounded, sharded real-time snapshots and minute
// accumulators. It contains no database or transport concerns.
package telemetry

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/protocol/telemetryv3"
)

const (
	ShardCount          = 256
	MaxSamplesPerMinute = 120
	MaxGPUDevices       = 64
	// MaxPendingWindowsPerNode bounds offline v3 replay memory while allowing a
	// useful chunk of historical minutes to be committed in one writer batch.
	MaxPendingWindowsPerNode = 16
	SnapshotTTL              = time.Minute
	topFraction              = 0.3
	topCapacity              = int(MaxSamplesPerMinute * topFraction)
)

var (
	ErrSampleLimit       = fmt.Errorf("telemetry sample limit of %d per minute exceeded", MaxSamplesPerMinute)
	ErrTooManyGPUDevices = fmt.Errorf("telemetry GPU device limit of %d exceeded", MaxGPUDevices)
	ErrWindowBacklog     = fmt.Errorf("telemetry accumulator has %d undrained minute windows", MaxPendingWindowsPerNode)
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
	latest                     common.Report
	latestAt                   time.Time
	windows                    map[int64]*accumulator
	v3NetworkUp, v3NetworkDown uint64
	v3NetworkAt                time.Time
	hasV3Network               bool
}

type Aggregate struct {
	WindowStart time.Time
	WindowEnd   time.Time
	Record      models.Record
	GPURecords  []models.GPURecord
	Sequence    uint64
}

func NewStore() *Store {
	store := &Store{now: time.Now}
	for i := range store.shards {
		store.shards[i].nodes = make(map[string]*nodeState)
	}
	return store
}

func (s *Store) Add(uuid string, report common.Report) error {
	return s.add(uuid, report, telemetryv3.Envelope{}, 0)
}

// AddV3 expands a bounded aggregate envelope into the minute accumulator while
// retaining its extrema and exact arithmetic mean. Sequence is carried to the
// durable writer so the database checkpoint commits in the same transaction as
// the history row represented by this frame.
func (s *Store) AddV3(uuid string, report common.Report, envelope telemetryv3.Envelope, sequence uint64) error {
	if sequence == 0 || envelope.Count == 0 || envelope.Count > MaxSamplesPerMinute {
		return errors.New("invalid telemetry v3 aggregate")
	}
	return s.add(uuid, report, envelope, sequence)
}

func (s *Store) add(uuid string, report common.Report, envelope telemetryv3.Envelope, sequence uint64) error {
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
	if sequence > 0 {
		adjusted, up, down := state.prepareV3Network(report, envelope)
		if err := target.addEnvelope(adjusted, envelope, sequence); err != nil {
			return err
		}
		state.v3NetworkUp, state.v3NetworkDown = up, down
		state.v3NetworkAt = report.UpdatedAt
		state.hasV3Network = true
		return nil
	}
	return target.add(report)
}

func (state *nodeState) prepareV3Network(report common.Report, envelope telemetryv3.Envelope) (common.Report, uint64, uint64) {
	up := uint64(max(report.Network.TotalUp, 0))
	down := uint64(max(report.Network.TotalDown, 0))
	if !state.hasV3Network {
		return report, up, down
	}
	up = saturatingAddUint64(state.v3NetworkUp, envelope.NetworkUpDelta)
	down = saturatingAddUint64(state.v3NetworkDown, envelope.NetworkDownDelta)
	report.Network.TotalUp = uint64ToInt64Saturated(up)
	report.Network.TotalDown = uint64ToInt64Saturated(down)
	elapsed := report.UpdatedAt.Sub(state.v3NetworkAt)
	if elapsed > 0 {
		seconds := elapsed.Seconds()
		report.Network.Up = boundedNetworkRate(envelope.NetworkUpDelta, seconds)
		report.Network.Down = boundedNetworkRate(envelope.NetworkDownDelta, seconds)
	}
	return report, up, down
}

func boundedNetworkRate(delta uint64, seconds float64) int64 {
	if seconds <= 0 {
		return 0
	}
	rate := float64(delta) / seconds
	if rate >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(rate)
}

func saturatingAddUint64(left, right uint64) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}

func (state *nodeState) accumulatorFor(window time.Time) (*accumulator, error) {
	key := window.Unix()
	if accumulator := state.windows[key]; accumulator != nil {
		return accumulator, nil
	}
	if len(state.windows) >= MaxPendingWindowsPerNode {
		return nil, ErrWindowBacklog
	}
	if state.windows == nil {
		state.windows = make(map[int64]*accumulator, 2)
	}
	state.windows[key] = newAccumulator()
	return state.windows[key], nil
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
			for unix, accumulator := range state.windows {
				start := time.Unix(unix, 0).UTC()
				if start.Before(boundary) {
					pending = append(pending, detached{uuid: uuid, start: start, acc: accumulator})
					delete(state.windows, unix)
				}
			}
			if len(state.windows) == 0 && cutoff.Sub(state.latestAt) > SnapshotTTL {
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
		result = append(result, Aggregate{WindowStart: item.start, WindowEnd: persistedAt, Record: record, GPURecords: gpu, Sequence: item.acc.maxSequence})
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

// Remove discards live and not-yet-durable telemetry for a deleted node. The
// administrator delete path closes its connection first, so stale history can
// never be flushed after the control-plane row has been removed.
func (s *Store) Remove(uuid string) {
	if uuid == "" {
		return
	}
	sh := s.shard(uuid)
	sh.mu.Lock()
	delete(sh.nodes, uuid)
	sh.mu.Unlock()
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
	maxSequence                    uint64
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
	a.addUnchecked(report)
	return nil
}

func (a *accumulator) addEnvelope(report common.Report, envelope telemetryv3.Envelope, sequence uint64) error {
	count := int(envelope.Count)
	if count <= 0 || a.count+count > MaxSamplesPerMinute {
		return ErrSampleLimit
	}
	if len(gpuDevices(report)) > MaxGPUDevices {
		return ErrTooManyGPUDevices
	}
	cpuMiddle := envelope.CPUSum
	ramMiddle := envelope.RAMUsedSum
	if count > 1 {
		cpuMiddle -= envelope.CPUMin + envelope.CPUMax
		ramMiddle -= min(ramMiddle, envelope.RAMUsedMin)
		ramMiddle -= min(ramMiddle, envelope.RAMUsedMax)
	}
	if count > 2 {
		cpuMiddle /= float64(count - 2)
		ramMiddle /= uint64(count - 2)
	}
	for index := 0; index < count; index++ {
		sample := report
		switch {
		case count == 1:
			sample.CPU.Usage = envelope.CPUSum
			sample.Ram.Used = uint64ToInt64Saturated(envelope.RAMUsedSum)
		case index == 0:
			sample.CPU.Usage = envelope.CPUMin
			sample.Ram.Used = uint64ToInt64Saturated(envelope.RAMUsedMin)
		case index == count-1:
			sample.CPU.Usage = envelope.CPUMax
			sample.Ram.Used = uint64ToInt64Saturated(envelope.RAMUsedMax)
		default:
			sample.CPU.Usage = cpuMiddle
			sample.Ram.Used = uint64ToInt64Saturated(ramMiddle)
		}
		a.addUnchecked(sample)
	}
	a.maxSequence = max(a.maxSequence, sequence)
	return nil
}

func uint64ToInt64Saturated(value uint64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if value > uint64(maxInt64) {
		return maxInt64
	}
	return int64(value)
}

func (a *accumulator) addUnchecked(report common.Report) {
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
