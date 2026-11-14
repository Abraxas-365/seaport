package schedulesvc

import (
	"context"
	"log/slog"
	"time"
)

// Runner fires due schedules every period (and once at start) until ctx
// ends. Every process runs one; the claim in Repository.Due makes each
// slot fire once across all of them.
func (s *Service) Runner(ctx context.Context, period time.Duration) {
	tick := func() {
		if _, err := s.FireDue(ctx, s.now()); err != nil && ctx.Err() == nil {
			slog.Error("fire due schedules", "err", err)
		}
	}
	tick()
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
