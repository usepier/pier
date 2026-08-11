package gcpgce

import (
	"github.com/usepier/pier/internal/driver"
)

// Machines is the TUI resize picker's catalog, filtered to the session's CPU
// architecture (resize can't cross arch: the disk's binaries live on).
// Costs are rough eu on-demand rates. Anything not listed here still works
// through `pier resize <session> <type>`.
func Machines(currentType string) []driver.Machine {
	if archOf(currentType) == "arm64" {
		return []driver.Machine{
			{Type: "t2a-standard-1", CPU: "1", Mem: "4", Cost: "~$0.04/h"},
			{Type: "t2a-standard-2", CPU: "2", Mem: "8", Cost: "~$0.08/h"},
			{Type: "t2a-standard-4", CPU: "4", Mem: "16", Cost: "~$0.15/h"},
			{Type: "t2a-standard-8", CPU: "8", Mem: "32", Cost: "~$0.31/h"},
			{Type: "c4a-standard-1", CPU: "1", Mem: "4", Cost: "~$0.04/h"},
			{Type: "c4a-standard-2", CPU: "2", Mem: "8", Cost: "~$0.09/h"},
			{Type: "c4a-standard-4", CPU: "4", Mem: "16", Cost: "~$0.18/h"},
			{Type: "c4a-standard-8", CPU: "8", Mem: "32", Cost: "~$0.36/h"},
		}
	}
	return []driver.Machine{
		{Type: "e2-small", CPU: "2", Mem: "2", Cost: "~$0.02/h"},
		{Type: "e2-medium", CPU: "2", Mem: "4", Cost: "~$0.04/h"},
		{Type: "e2-standard-2", CPU: "2", Mem: "8", Cost: "~$0.08/h"},
		{Type: "e2-standard-4", CPU: "4", Mem: "16", Cost: "~$0.16/h"},
		{Type: "e2-standard-8", CPU: "8", Mem: "32", Cost: "~$0.31/h"},
		{Type: "e2-standard-16", CPU: "16", Mem: "64", Cost: "~$0.62/h"},
	}
}

// Machines implements the driver interface by delegating to the package
// catalog.
func (d *Driver) Machines(currentType string) []driver.Machine {
	return Machines(currentType)
}
