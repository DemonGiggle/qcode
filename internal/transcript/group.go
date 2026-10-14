// Package transcript describes optional grouping for interactive output.
package transcript

import "io"

// GroupWriter creates a writer for one independent transcript group. Creating
// a group does not emit output; its first visible write establishes the boundary.
type GroupWriter interface {
	NewOutputGroup() io.Writer
}

// Grouped reports whether a writer uses interactive transcript spacing.
// Renderers can expose the capability of their underlying writer via this method.
func Grouped(out io.Writer) bool {
	if status, ok := out.(interface{ OutputGroupsEnabled() bool }); ok {
		return status.OutputGroupsEnabled()
	}
	_, ok := out.(GroupWriter)
	return ok
}

// NewGroup preserves ordinary writers' output behavior.
func NewGroup(out io.Writer) io.Writer {
	if writer, ok := out.(GroupWriter); ok && Grouped(out) {
		return writer.NewOutputGroup()
	}
	return out
}
