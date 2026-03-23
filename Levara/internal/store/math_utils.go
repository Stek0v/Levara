package store

func addToVector(a, b []float32) {
	for i := range a {
		a[i] += b[i]
	}
}

func divVector(a []float32, n float32) {
	if n == 0 {
		return
	}

	for i := range a {
		a[i] /= n
	}
}
