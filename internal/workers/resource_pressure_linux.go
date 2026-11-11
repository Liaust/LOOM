//go:build linux

package workers

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func SampleResourcePressure(now time.Time) ResourcePressureSnapshot {
	s := ResourcePressureSnapshot{SampledAt: now.UTC(), DiskPath: os.TempDir()}
	for _, metric := range []struct {
		path, kind string
		target     **float64
	}{
		{"/proc/pressure/cpu", "some", &s.CPUSomeAvg10},
		{"/proc/pressure/memory", "full", &s.MemoryFullAvg10},
		{"/proc/pressure/io", "full", &s.IOFullAvg10},
	} {
		data, err := os.ReadFile(metric.path)
		if err == nil {
			*metric.target = parsePSIAvg10(string(data), metric.kind)
		}
		if *metric.target == nil {
			s.UnknownMetrics = append(s.UnknownMetrics, metric.path)
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		s.AvailableMemoryBytes = parseAvailableMemory(string(data))
	}
	if s.AvailableMemoryBytes == nil {
		s.UnknownMetrics = append(s.UnknownMetrics, "available_memory")
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(s.DiskPath, &fs); err == nil && fs.Bsize > 0 && fs.Bavail <= uint64((1<<63)-1)/uint64(fs.Bsize) {
		value := int64(fs.Bavail) * int64(fs.Bsize)
		s.AvailableDiskBytes = &value
	}
	if s.AvailableDiskBytes == nil {
		s.UnknownMetrics = append(s.UnknownMetrics, "available_disk")
	}
	return s
}
