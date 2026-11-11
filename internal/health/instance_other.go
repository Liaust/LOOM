//go:build !linux

package health

func selfIdentity() *InstanceIdentity { return nil }
