package metrics

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SystemSample is one point-in-time reading of aggregate container resource use,
// sampled from `docker stats`. Host-level CPU/mem/disk are collected separately
// by node_exporter + cAdvisor into Prometheus; this cheap sampler exists so a
// run has a resource trace even when the monitoring stack is not up.
type SystemSample struct {
	T          time.Time `json:"t"`
	CPUPercent float64   `json:"cpu_percent"` // sum across matched containers
	MemBytes   uint64    `json:"mem_bytes"`   // sum across matched containers
	Containers int       `json:"containers"`
}

// SystemSampler polls `docker stats` at a fixed interval for containers whose
// name contains one of NamePrefixes.
type SystemSampler struct {
	Interval     time.Duration
	NamePrefixes []string

	mu      sync.Mutex
	samples []SystemSample
}

// Run blocks sampling until ctx is cancelled.
func (s *SystemSampler) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = time.Second
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if sample, ok := s.sample(ctx); ok {
				s.mu.Lock()
				s.samples = append(s.samples, sample)
				s.mu.Unlock()
			}
		}
	}
}

// Samples returns a copy of everything collected.
func (s *SystemSampler) Samples() []SystemSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SystemSample, len(s.samples))
	copy(out, s.samples)
	return out
}

type dockerStatsLine struct {
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
}

func (s *SystemSampler) sample(ctx context.Context) (SystemSample, bool) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "stats", "--no-stream", "--format", "{{json .}}").Output()
	if err != nil {
		return SystemSample{}, false
	}
	sample := SystemSample{T: time.Now()}
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		var d dockerStatsLine
		if json.Unmarshal([]byte(ln), &d) != nil {
			continue
		}
		if !s.match(d.Name) {
			continue
		}
		sample.Containers++
		sample.CPUPercent += parsePercent(d.CPUPerc)
		sample.MemBytes += parseMemUsed(d.MemUsage)
	}
	return sample, sample.Containers > 0
}

func (s *SystemSampler) match(name string) bool {
	if len(s.NamePrefixes) == 0 {
		return true
	}
	for _, p := range s.NamePrefixes {
		if strings.Contains(name, p) {
			return true
		}
	}
	return false
}

func parsePercent(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
	return f
}

// parseMemUsed parses the "123.4MiB / 2GiB" form and returns the used side in bytes.
func parseMemUsed(s string) uint64 {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 0 {
		return 0
	}
	return parseSize(strings.TrimSpace(parts[0]))
}

func parseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "GiB"):
		mult, s = 1<<30, strings.TrimSuffix(s, "GiB")
	case strings.HasSuffix(s, "MiB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MiB")
	case strings.HasSuffix(s, "KiB"):
		mult, s = 1<<10, strings.TrimSuffix(s, "KiB")
	case strings.HasSuffix(s, "GB"):
		mult, s = 1e9, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1e6, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "kB"), strings.HasSuffix(s, "KB"):
		mult, s = 1e3, strings.TrimSuffix(strings.TrimSuffix(s, "KB"), "kB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return uint64(f * float64(mult))
}
