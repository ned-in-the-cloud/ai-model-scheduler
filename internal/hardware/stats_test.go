package hardware

import (
	"testing"
)

func TestLatestSample(t *testing.T) {
	tail := `{"ts":100,"gpus":[{"vendor":"amd","pci":"0000:03:00.0","util_percent":10,"vram_used_bytes":1,"vram_total_bytes":2,"temp_c":40,"power_w":50}]}
{"ts":105,"gpus":[{"vendor":"amd","pci":"0000:03:00.0","util_percent":88,"vram_used_bytes":8000000000,"vram_total_bytes":16000000000,"temp_c":71,"power_w":190},{"vendor":"nvidia","name":"NVIDIA GeForce RTX 3090","util_percent":45,"vram_used_bytes":10737418240,"vram_total_bytes":25769803776,"temp_c":62,"power_w":250}]}
`
	sample, ok := latestSample(tail)
	if !ok {
		t.Fatal("no sample parsed")
	}
	if sample.TS != 105 {
		t.Errorf("ts = %d, want newest sample 105", sample.TS)
	}
	if len(sample.GPUs) != 2 {
		t.Fatalf("gpus = %d, want 2", len(sample.GPUs))
	}
	if sample.GPUs[0].UtilPercent != 88 || sample.GPUs[1].Name != "NVIDIA GeForce RTX 3090" {
		t.Errorf("sample: %+v", sample.GPUs)
	}

	// A truncated first line (log tail cut mid-line) must not break parsing.
	if s, ok := latestSample(`total":2}]}` + "\n" + tail); !ok || s.TS != 105 {
		t.Errorf("truncated tail: ok=%v ts=%d", ok, s.TS)
	}

	if _, ok := latestSample(""); ok {
		t.Error("empty tail should not parse")
	}
}

func TestEnrichNames(t *testing.T) {
	svc := &Service{}
	svc.gpus = []GPU{
		{Vendor: "amd", Name: "AMD Radeon R9700", PCI: "0000:03:00.0"},
		{Vendor: "nvidia", Name: "NVIDIA GeForce RTX 3090", PCI: "0000:01:00.0"},
	}

	got := svc.enrichNames([]GPUStats{
		{Vendor: "amd", PCI: "0000:03:00.0", VRAMUsed: 4, VRAMTotal: 16},
		{Vendor: "nvidia", Name: "NVIDIA GeForce RTX 3090"},
	})

	if got[0].Name != "AMD Radeon R9700" {
		t.Errorf("amd name = %q, want probe name (unique vendor match)", got[0].Name)
	}
	if got[0].VRAMPercent != 25 {
		t.Errorf("vram percent = %v, want 25", got[0].VRAMPercent)
	}
	if got[1].Name != "NVIDIA GeForce RTX 3090" {
		t.Errorf("nvidia name overwritten: %q", got[1].Name)
	}

	// Two GPUs of the same vendor: fall back to vendor+PCI, never guess.
	svc.gpus = append(svc.gpus, GPU{Vendor: "amd", Name: "Second AMD", PCI: "0000:04:00.0"})
	got = svc.enrichNames([]GPUStats{{Vendor: "amd", PCI: "0000:03:00.0"}})
	if got[0].Name != "amd 0000:03:00.0" {
		t.Errorf("ambiguous vendor name = %q, want fallback", got[0].Name)
	}
}
