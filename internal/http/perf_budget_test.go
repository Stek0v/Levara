package http

import "os"

func enforcePerfBudgets() bool {
	return os.Getenv("LEVARA_ENFORCE_PERF_BUDGETS") == "1"
}
