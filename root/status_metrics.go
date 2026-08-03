package root

func percent(used, total float64) float64 {
	if used <= 0 || total <= 0 {
		return 0
	}
	value := used / total * 100
	if value > 100 {
		return 100
	}
	return value
}

func cpuPercent(previousTotal, previousIdle, total, idle uint64) float64 {
	if total <= previousTotal || idle < previousIdle {
		return 0
	}
	deltaTotal := total - previousTotal
	deltaIdle := idle - previousIdle
	if deltaIdle > deltaTotal {
		return 0
	}
	return percent(float64(deltaTotal-deltaIdle), float64(deltaTotal))
}
