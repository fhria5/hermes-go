package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSystemInfo(t *testing.T) {
	tr := &SystemInfoTool{Services: []string{"hermes-go"}}
	res, err := tr.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run gagal: %v", err)
	}
	out := res.Output
	for _, want := range []string{"Host:", "RAM:", "Disk", "CPU:", "Uptime:", "Service hermes-go:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output tidak memuat %q\nout:\n%s", want, out)
		}
	}
	t.Logf("\n%s", out)
}
