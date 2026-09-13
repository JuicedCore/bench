package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
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

// ContainerFailure is a platform container that stopped running mid-run, or had
// a process OOM-killed while the container itself kept running (YugabyteDB
// restarts its postgres child that way, but the Committing Peer's SQL connection
// dies with it). A benchmark whose platform lost a node measured a broken network
// from that point on, so the engine treats it as a platform failure rather than
// as load results.
type ContainerFailure struct {
	Name string `json:"name"`
	// At is when the failure was observed: the OOM event time, or for an exit
	// the last sample the container was still running in.
	At        time.Time `json:"at"`
	OOMKilled bool      `json:"oom_killed"`
	Exited    bool      `json:"exited"`
	ExitCode  int       `json:"exit_code,omitempty"`
	Status    string    `json:"status,omitempty"` // docker State.Status, or "removed"
}

func (e ContainerFailure) String() string {
	switch {
	case e.Exited && e.OOMKilled:
		return fmt.Sprintf("%s was OOM-killed at its memory limit and exited (after %s, exit code %d)", e.Name, e.At.Format("15:04:05"), e.ExitCode)
	case e.Exited:
		return fmt.Sprintf("%s exited (after %s, status %s, exit code %d)", e.Name, e.At.Format("15:04:05"), e.Status, e.ExitCode)
	default:
		return fmt.Sprintf("%s had a process OOM-killed at its memory limit at %s", e.Name, e.At.Format("15:04:05"))
	}
}

// SystemSampler polls `docker stats` at a fixed interval for containers whose
// name contains one of NamePrefixes.
type SystemSampler struct {
	Interval     time.Duration
	NamePrefixes []string

	mu      sync.Mutex
	samples []SystemSample
	// lastSeen is when each matched container was last running; missed counts
	// consecutive samples it has been absent from. failures holds confirmed
	// failures, at most one per container.
	lastSeen map[string]time.Time
	missed   map[string]int
	failures []ContainerFailure
}

// Failures returns the platform containers that failed since sampling began.
func (s *SystemSampler) Failures() []ContainerFailure {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ContainerFailure, len(s.failures))
	copy(out, s.failures)
	return out
}

// record merges f into the failure list: an OOM event and the exit it caused
// are one failure.
func (s *SystemSampler) record(f ContainerFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.failures {
		if s.failures[i].Name == f.Name {
			cur := &s.failures[i]
			cur.OOMKilled = cur.OOMKilled || f.OOMKilled
			if f.Exited {
				cur.Exited, cur.ExitCode, cur.Status = true, f.ExitCode, f.Status
			}
			return
		}
	}
	s.failures = append(s.failures, f)
}

// watchOOM records every OOM kill docker reports for a matched container until
// ctx ends. Sampling cannot see these when the container survives the kill.
func (s *SystemSampler) watchOOM(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "docker", "events", "--filter", "event=oom",
		"--format", "{{.Actor.Attributes.name}}|{{.TimeNano}}")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if cmd.Start() != nil {
		return
	}
	defer func() { _ = cmd.Wait() }()
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		name, ts, _ := strings.Cut(strings.TrimSpace(sc.Text()), "|")
		if name == "" || !s.match(name) {
			continue
		}
		at := time.Now()
		if ns, err := strconv.ParseInt(ts, 10, 64); err == nil {
			at = time.Unix(0, ns)
		}
		s.record(ContainerFailure{Name: name, At: at, OOMKilled: true})
	}
}

// Run blocks sampling until ctx is cancelled.
func (s *SystemSampler) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = time.Second
	}
	go s.watchOOM(ctx)
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sample, names, ok := s.sample(ctx)
			if !ok {
				continue
			}
			if sample.Containers > 0 {
				s.mu.Lock()
				s.samples = append(s.samples, sample)
				s.mu.Unlock()
			}
			// Track even an empty sample: every container having stopped is
			// the case exit detection most needs to see.
			s.track(ctx, sample.T, names)
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

// track updates container liveness from one sample's running names. A container
// must be absent from two consecutive samples before it counts as stopped, and
// docker must agree it is not running, so a flaky `docker stats` line cannot
// fail a run. It is inspected at that moment because teardown removes it.
func (s *SystemSampler) track(ctx context.Context, t time.Time, running []string) {
	s.mu.Lock()
	if s.lastSeen == nil {
		s.lastSeen, s.missed = map[string]time.Time{}, map[string]int{}
	}
	present := map[string]bool{}
	for _, n := range running {
		present[n] = true
		s.lastSeen[n] = t
		delete(s.missed, n)
	}
	var gone []string
	for n := range s.lastSeen {
		if present[n] {
			continue
		}
		s.missed[n]++
		if s.missed[n] == 2 {
			gone = append(gone, n)
		}
	}
	s.mu.Unlock()
	sort.Strings(gone)

	for _, n := range gone {
		e, stillRunning := inspectExit(ctx, n)
		s.mu.Lock()
		e.At = s.lastSeen[n]
		delete(s.missed, n)
		if !stillRunning {
			delete(s.lastSeen, n)
		}
		s.mu.Unlock()
		if !stillRunning {
			s.record(e)
		}
	}
}

func inspectExit(ctx context.Context, name string) (ContainerFailure, bool) {
	e := ContainerFailure{Name: name, Exited: true, Status: "removed", ExitCode: -1}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "inspect", "-f",
		"{{.State.Running}}|{{.State.OOMKilled}}|{{.State.ExitCode}}|{{.State.Status}}", name).Output()
	if err != nil {
		return e, false
	}
	f := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(f) != 4 {
		return e, false
	}
	if f[0] == "true" {
		return e, true
	}
	e.OOMKilled = f[1] == "true"
	e.ExitCode, _ = strconv.Atoi(f[2])
	e.Status = f[3]
	return e, false
}

func (s *SystemSampler) sample(ctx context.Context) (SystemSample, []string, bool) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "stats", "--no-stream", "--format", "{{json .}}").Output()
	if err != nil {
		return SystemSample{}, nil, false
	}
	sample := SystemSample{T: time.Now()}
	var names []string
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
		names = append(names, d.Name)
	}
	return sample, names, true
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
