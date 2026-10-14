package delivery

import (
	"context"
	"math/rand/v2"
	"time"
)

// Pause applies the delivery profile's full jitter with a saturated exponent.
func Pause(ctx context.Context, attempt int) error {
	ceiling := min(30*time.Second, 250*time.Millisecond*time.Duration(1<<min(attempt, 7)))
	timer := time.NewTimer(time.Duration(rand.Int64N(int64(ceiling) + 1)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
