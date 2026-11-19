//go:build !linux

package backupauto

// dirFreeBytes reports unknown free space on platforms without the Linux
// statfs contract. Callers treat unknown (0, nil) as "skip the precheck":
// the spool write itself surfaces a real ENOSPC.
func dirFreeBytes(_ string) (uint64, error) { return 0, nil }
