//go:build race

package outline

import "time"

const parsePerfBudget = 500 * time.Millisecond
