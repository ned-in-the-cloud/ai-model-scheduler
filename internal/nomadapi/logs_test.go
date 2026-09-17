package nomadapi

import "testing"

func TestNormalizePodmanLogs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{
			name: "docker output passes through",
			in:   "line one\nline two\n",
			want: "line one\nline two\n",
		},
		{
			name: "podman full lines stripped",
			in: "2026-09-17T17:57:25.585794651+00:00 stdout F downloading model\n" +
				"2026-09-17T17:58:09.075555352+00:00 stderr F Killed\n",
			want: "downloading model\nKilled\n",
		},
		{
			name: "partial chunks joined into one line",
			in: "2026-09-17T17:57:25.840312034+00:00 stderr P Fetching 1 files:   0%\n" +
				"2026-09-17T17:57:26.000000000+00:00 stderr F  done\n",
			want: "Fetching 1 files:   0% done\n",
		},
		{
			name: "json manifest line survives",
			in:   `2026-09-17T18:00:00.000000000+00:00 stdout F {"kind":"gguf","path":"a.gguf","size_bytes":5}` + "\n",
			want: `{"kind":"gguf","path":"a.gguf","size_bytes":5}` + "\n",
		},
		{
			name: "non-timestamp line mentioning stdout untouched",
			in:   "INFO stdout F is not a podman prefix\n",
			want: "INFO stdout F is not a podman prefix\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePodmanLogs(tt.in); got != tt.want {
				t.Errorf("normalizePodmanLogs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
