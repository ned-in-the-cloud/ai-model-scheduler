package bench

import (
	"bytes"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }

var taskNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// ParseSuiteYAML decodes a suite definition such as:
//
//	name: qwen-vllm-sweep
//	configs:
//	  - label: fp16-8k
//	    runtime: vllm
//	    model: Qwen/Qwen2.5-7B-Instruct
//	    gpu: nvidia
//	    ctx_size: 8192
//	    extra_args: "--gpu-memory-utilization 0.9"
//
// Field names match the deploy form / JSON API.
func ParseSuiteYAML(data []byte) (*Suite, error) {
	var suite Suite
	dec := yaml.NewDecoder(bytesReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&suite); err != nil {
		return nil, fmt.Errorf("parsing suite yaml: %w", err)
	}
	if err := suite.Validate(); err != nil {
		return nil, err
	}
	return &suite, nil
}

// SuiteYAML renders a suite back to YAML for download or editing.
func SuiteYAML(s Suite) ([]byte, error) {
	return yaml.Marshal(s)
}
