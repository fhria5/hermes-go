package tools

// Tahap C+: SystemInfo — ringkasan kondisi host (RAM, disk, CPU, suhu, uptime,
// status service systemd). Read-only, tanpa shell: semua data dari /proc, /sys,
// syscall, dan `systemctl is-active` (exec langsung tanpa shell, path tetap).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type SystemInfoTool struct {
	// Services: daftar unit systemd yang statusnya dilaporkan (mis. ["hermes-go"]).
	Services []string
}

func (t *SystemInfoTool) Name() string { return "system_info" }
func (t *SystemInfoTool) Description() string {
	return "Ringkasan kondisi host: RAM, disk, CPU, suhu, uptime, status service."
}
func (t *SystemInfoTool) Permission() Permission { return PermAuto }
func (t *SystemInfoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *SystemInfoTool) Run(ctx context.Context, _ json.RawMessage) (Result, error) {
	var sb strings.Builder

	// --- Host & uptime ---
	hostname, _ := os.Hostname()
	sb.WriteString(fmt.Sprintf("Host: %s (%s/%s)\n", hostname, runtime.GOOS, runtime.GOARCH))

	// --- RAM dari /proc/meminfo ---
	if mi, err := parseMemInfo(); err == nil {
		sb.WriteString(fmt.Sprintf("RAM: %s terpakai / %s total (tersedia %s)\n",
			humanBytes(mi["MemAvailable"]), humanBytes(mi["MemTotal"]),
			humanBytes(mi["MemAvailable"])))
	}

	// --- Disk workspace root (atau /) ---
	var st syscall.Statfs_t
	path := "/"
	if err := syscall.Statfs(path, &st); err == nil {
		total := uint64(st.Blocks) * uint64(st.Bsize)
		free := uint64(st.Bavail) * uint64(st.Bsize)
		used := total - free
		sb.WriteString(fmt.Sprintf("Disk %s: %s terpakai / %s total (bebas %s)\n",
			path, humanBytes(used), humanBytes(total), humanBytes(free)))
	}

	// --- CPU: load average + jumlah core ---
	sb.WriteString(fmt.Sprintf("CPU: %d core, load %s\n", runtime.NumCPU(), loadAvg()))

	// --- Suhu thermal zone (jika ada) ---
	if temps := readTemps(); len(temps) > 0 {
		sb.WriteString("Suhu: " + strings.Join(temps, ", ") + "\n")
	} else {
		sb.WriteString("Suhu: tidak tersedia\n")
	}

	// --- Uptime ---
	if up, err := uptime(); err == nil {
		sb.WriteString(fmt.Sprintf("Uptime: %s\n", up))
	}

	// --- Status service systemd (exec langsung, tanpa shell) ---
	for _, svc := range t.Services {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		out, err := exec.CommandContext(cctx, "systemctl", "is-active", svc).Output()
		cancel()
		status := strings.TrimSpace(string(out))
		if err != nil && status == "" {
			status = "tidak diketahui"
		}
		sb.WriteString(fmt.Sprintf("Service %s: %s\n", svc, status))
	}

	return Result{Output: sb.String()}, nil
}

// parseMemInfo membaca /proc/meminfo ke map (kB).
func parseMemInfo() (map[string]uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	m := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		var v uint64
		if _, err := fmt.Sscanf(parts[1], "%d", &v); err == nil {
			m[strings.TrimSuffix(parts[0], ":")] = v * 1024 // kB → bytes
		}
	}
	return m, nil
}

// readTemps membaca /sys/class/thermal/thermal_zone*/temp (millicelsius).
func readTemps() []string {
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "thermal_zone") {
			continue
		}
		tb, err := os.ReadFile("/sys/class/thermal/" + e.Name() + "/temp")
		if err != nil {
			continue
		}
		var mc int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(tb)), "%d", &mc); err != nil {
			continue
		}
		name := e.Name()
		if nb, err := os.ReadFile("/sys/class/thermal/" + e.Name() + "/type"); err == nil {
			name = strings.TrimSpace(string(nb))
		}
		out = append(out, fmt.Sprintf("%s %.0f°C", name, float64(mc)/1000))
	}
	return out
}

// uptime dari /proc/uptime.
func uptime() (string, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "", err
	}
	var secs float64
	if _, err := fmt.Sscanf(strings.Fields(string(b))[0], "%f", &secs); err != nil {
		return "", err
	}
	d := time.Duration(secs * float64(time.Second))
	return fmt.Sprintf("%dd %dh %dm", int(d.Hours()/24), int(d.Hours())%24, int(d.Minutes())%60), nil
}

// loadAvg dari /proc/loadavg.
func loadAvg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "?"
	}
	f := strings.Fields(string(b))
	if len(f) >= 3 {
		return f[0] + " " + f[1] + " " + f[2]
	}
	return "?"
}

// humanBytes format sederhana.
func humanBytes(v uint64) string {
	const k = 1024
	switch {
	case v >= k*k*k:
		return fmt.Sprintf("%.1f GiB", float64(v)/(k*k*k))
	case v >= k*k:
		return fmt.Sprintf("%.0f MiB", float64(v)/(k*k))
	case v >= k:
		return fmt.Sprintf("%.0f KiB", float64(v)/k)
	default:
		return fmt.Sprintf("%d B", v)
	}
}
