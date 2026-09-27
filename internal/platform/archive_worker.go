package platform

import (
	"context"
	"log/slog"
	"time"
)

func (s *Service) archiveWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 30*time.Second)
		var err error
		for work.Err() == nil {
			var count int
			count, err = s.PurgeArchives(work)
			if err != nil || count < 100 {
				break
			}
		}
		if err == nil {
			err = work.Err()
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("history retention batch failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
