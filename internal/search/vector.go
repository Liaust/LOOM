package search

func VectorDistanceScore(distance float64) float64 {
	if distance < 0 {
		distance = 0
	}
	return 1 / (1 + distance)
}
