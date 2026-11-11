package minidashboard

import "testing"

func TestWorkspaceCapacityUsesFilesystemCounters(t *testing.T) {
	v, err := platformFilesystemCapacity(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	if v.TotalBytes == 0 || v.FreeBytes > v.TotalBytes || v.AvailableBytes > v.FreeBytes || v.UsedBytes+v.FreeBytes != v.TotalBytes || v.Filesystem == "" {
		t.Fatalf("invalid capacity: %+v", v)
	}
}
