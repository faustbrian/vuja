//go:build !darwin && !linux

package root

func sampleSystemUsage() (float64, float64) {
	return 0, 0
}
