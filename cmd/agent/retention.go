// Retention worker: jalankan ApplyMemoryRetention berkala.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"hermes-go/internal/storage"
)

// RunRetentionWorker: pekerja berkala MEMORY_RETENTION_DAYS.
// Konvensi konfigurasi (didokumentasikan di README):
//   - 0 atau tidak disetel → retensi nonaktif (tidak menghapus apa pun).
//   - >0 → memory dengan updated_at lebih tua dari N hari dihapus (batch 500).
//   - RETENTION_INTERVAL (durasi Go, default 6h) = jeda antar siklus.
//
// Worker berjalan di goroutine tunggal; tidak ada race dengan penulisan memory
// karena SQLite Store memakai MaxOpenConns(1) + transaksi.
func RunRetentionWorker(ctx context.Context, store *storage.Store, retentionDays int, log *slog.Logger) {
	if retentionDays <= 0 {
		log.Info("retensi memory nonaktif (MEMORY_RETENTION_DAYS<=0)")
		return
	}
	interval := 6 * time.Hour
	if v := os.Getenv("RETENTION_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	log.Info("retensi memory aktif", "days", retentionDays, "interval", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	run := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
		res, err := store.ApplyMemoryRetention(cutoff, 500)
		if err != nil {
			log.Error("retensi memory gagal", "err", err)
			return
		}
		if res.MemoryDeleted > 0 {
			log.Info("retensi memory", "dihapus", res.MemoryDeleted, "cutoff", cutoff)
		}
	}
	run() // sekali saat mulai
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
