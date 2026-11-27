//go:build !race

package outline

import "time"

const parsePerfBudget = 100 * time.Millisecond
