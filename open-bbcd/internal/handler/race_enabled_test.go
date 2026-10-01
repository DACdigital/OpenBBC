//go:build race

package handler

// Under the race detector bytes.Buffer's growth is not fused into one
// allocation, so allocation counts double (see the single-buffer test).
func init() { raceAllocFactor = 2 }
