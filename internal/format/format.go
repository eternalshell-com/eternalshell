// Package format turns numbers into short display text.
package format

import "fmt"

// Bytes formats a size as short text, e.g. "512B", "90.0M" or "2.6G".
func Bytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(b)/float64(div), "KMGTPE"[exp])
}

// Percent formats a CPU or usage percentage without decimals, e.g. "382%".
func Percent(p float64) string {
	return fmt.Sprintf("%.0f%%", p)
}
