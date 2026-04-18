// Package command is the write side of the NDMS integration layer.
//
// Plan 1 contains only SaveCoordinator, which debounces flash-write Save
// requests emitted by individual command groups. Later plans add the
// per-resource Command groups (InterfaceCommands, PolicyCommands, etc.).
package command
