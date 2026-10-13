// Package scheduler: scheduler persisten berbasis SQLite.
// Kebijakan missed-run eksplisit: interval → satu eksekusi + next = now+interval
// (tidak mengejar backlog); once yang terlewat > grace (24 jam) → nonaktif,
// bukan dijalankan membabi buta.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"hermes-go/internal/storage"
)

const onceGrace = 24 * time.Hour

type Scheduler struct {
	store  *storage.Store
	runJob func(prompt, sessionID string)
	log    *slog.Logger
	ticker *time.Ticker
}

func New(store *storage.Store, runJob func(prompt, sessionID string)) *Scheduler {
	return &Scheduler{store: store, runJob: runJob, log: slog.Default(), ticker: time.NewTicker(30 * time.Second)}
}

// computeNext: kebijakan next_run_at. Interval: next dihitung dari `now`
// (bukan dari prev) → backlog terlewat tidak dieksekusi beruntun; satu tick
// = satu eksekusi, next = now + interval.
func computeNext(sc storage.Schedule, now time.Time) string {
	switch sc.Kind {
	case "interval":
		return now.Add(time.Duration(sc.IntervalSec) * time.Second).UTC().Format(time.RFC3339Nano)
	default: // once
		return "" // selesai
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-s.ticker.C:
			s.tick(ctx, now)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	due, err := s.store.DueSchedules(now, computeNext)
	if err != nil {
		s.log.Error("scheduler query gagal", "err", err)
		return
	}
	for _, sc := range due {
		// Missed-run once: terlalu lama terlewat → nonaktifkan, jangan jalankan.
		if sc.Kind == "once" {
			if runAt, err := time.Parse(time.RFC3339, sc.NextRunAt); err == nil && now.Sub(runAt) > onceGrace {
				if _, err := s.store.SetScheduleEnabled(sc.ID, sc.SessionID, false); err != nil {
					s.log.Error("nonaktifkan jadwal gagal", "err", err)
				}
				_ = s.store.SetScheduleResult(sc.ID, "skipped: missed > 24h")
				s.log.Warn("jadwal once terlewat dinonaktifkan", "id", sc.ID)
				continue
			}
		}
		s.log.Info("scheduler menjalankan job", "id", sc.ID, "name", sc.Name)
		s.runJob(sc.Prompt, sc.SessionID)
	}
}
