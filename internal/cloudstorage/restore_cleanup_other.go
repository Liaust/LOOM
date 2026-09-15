//go:build !linux && !darwin

package cloudstorage

import "fmt"

func cleanupLeafFlags(uint32) int         { return 0 }
func cleanupMountID(int) (string, error)  { return "", fmt.Errorf("cleanup platform unsupported") }
func cleanupDirectoryACL(int, bool) error { return fmt.Errorf("cleanup platform unsupported") }

func cleanupPayloadACL(int) error { return fmt.Errorf("cleanup platform unsupported") }

func cleanupRenameNoReplace(int, string, int, string) error {
	return fmt.Errorf("unsupported quarantine rename")
}
func cleanupPrivateDirectoryACL(int) error { return fmt.Errorf("unsupported private ACL") }
func cleanupReadPayloadDirACL(int) (cleanupACLState, error) {
	return cleanupACLState{}, fmt.Errorf("unsupported payload ACL")
}
func cleanupSealedACL(original cleanupACLState, mode uint32) cleanupACLState { return original }
