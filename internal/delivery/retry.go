package delivery

import (
	"context"
	"math/rand/v2"
	"time"
)

// Pause applies the delivery profile's full jitter with a saturated exponent.
func Delay(attempt int) time.Duration {
	ceiling := min(30*time.Second, 250*time.Millisecond*time.Duration(1<<min(attempt, 7)))
	return time.Duration(rand.Int64N(int64(ceiling) + 1))
}
func Pause(ctx context.Context, attempt int) error {
	timer := time.NewTimer(Delay(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
