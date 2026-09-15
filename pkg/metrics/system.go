package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	// Logger receives sampling problems as they happen. nil = slog.Default().
	Logger *slog.Logger

	// Health bookkeeping, guarded by mu: a sampler that cannot talk to docker
	// silently detects nothing, so every way it can go blind is recorded and
	// surfaced by Unhealthy.
	ticks, statsFailures, consecFail, maxConsecFail int
	lastStatsErr                                    string
	badLines                                        int
	oomWatchErr                                     string
	inspectErrs                                     int

	mu      sync.Mutex
	samples []SystemSample
	// lastSeen is when each matched container was last running; missed counts
	// consecutive samples it has been absent from. failures holds confirmed
	// failures, at most one per container.
	lastSeen map[string]time.Time
	missed   map[string]int
	failures []ContainerFailure
}

func (s *SystemSampler) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// unhealthyConsecutive is how many docker stats calls in a row must fail before
// the sampler counts as blind: over that span a container exit cannot be seen.
const unhealthyConsecutive = 3

// Unhealthy returns "" when sampling worked, otherwise a one-line description of
// every way it was degraded during the run. The engine turns it into a caveat.
func (s *SystemSampler) Unhealthy() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var why []string
	if len(s.NamePrefixes) == 0 {
		why = append(why, "system_metrics.container_names is empty, so no container was sampled or watched")
	}
	if s.maxConsecFail >= unhealthyConsecutive {
		why = append(why, fmt.Sprintf("docker stats failed %d of %d samples, up to %d in a row (last error: %s)",
			s.statsFailures, s.ticks, s.maxConsecFail, s.lastStatsErr))
	}
	if s.oomWatchErr != "" {
		why = append(why, "OOM watch (docker events) unavailable: "+s.oomWatchErr)
	}
	if s.inspectErrs > 0 {
		why = append(why, fmt.Sprintf("docker inspect failed %d time(s) while confirming a missing container", s.inspectErrs))
	}
	return strings.Join(why, "; ")
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
	var stderr strings.Builder
	cmd.Stderr = &stderr
	fail := func(err error) {
		msg := err.Error()
		if e := strings.TrimSpace(stderr.String()); e != "" {
			msg += ": " + e
		}
		s.mu.Lock()
		s.oomWatchErr = msg
		s.mu.Unlock()
		s.log().Warn("OOM watch unavailable: an OOM kill that does not stop the container will not be detected", "err", msg)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		fail(err)
		return
	}
	if err := cmd.Start(); err != nil {
		fail(fmt.Errorf("start docker events: %w", err))
		return
	}
	defer func() {
		// Exiting because the run ended is normal; anything else means the
		// watch died mid-run.
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			fail(fmt.Errorf("docker events exited: %w", err))
		}
	}()
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
	if len(s.NamePrefixes) == 0 {
		s.log().Warn("system_metrics.container_names is empty: sampler is idle, so container failures will not be detected")
		return
	}
	go s.watchOOM(ctx)
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sample, names, err := s.sample(ctx)
			if ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			s.ticks++
			if err != nil {
				s.statsFailures++
				s.consecFail++
				s.maxConsecFail = max(s.maxConsecFail, s.consecFail)
				s.lastStatsErr = err.Error()
			} else {
				s.consecFail = 0
			}
			consec, total := s.consecFail, s.statsFailures
			s.mu.Unlock()
			if err != nil {
				// Say it on the first failure and when it becomes a blind
				// spot, then only occasionally, to keep the log readable.
				if total == 1 || consec == unhealthyConsecutive || total%60 == 0 {
					s.log().Warn("docker stats failed; container sampling and exit detection are blind while this lasts",
						"err", err, "consecutive", consec, "total_failures", total)
				}
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
		e, stillRunning, ierr := inspectExit(ctx, n)
		if ierr != nil {
			// Could not ask docker: do not declare a platform failure on a
			// docker hiccup. Keep it as seen; if it is really gone it will be
			// missed again and re-inspected.
			s.mu.Lock()
			s.inspectErrs++
			delete(s.missed, n)
			s.mu.Unlock()
			s.log().Warn("container missing from docker stats but docker inspect failed; not counting it as failed yet",
				"container", n, "err", ierr)
			continue
		}
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

// inspectExit asks docker why a container disappeared from docker stats. It
// returns stillRunning=true when docker says it is running, and a non-nil error
// when docker could not answer (daemon hiccup, timeout) - which is NOT evidence
// the container failed.
func inspectExit(ctx context.Context, name string) (ContainerFailure, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "inspect", "-f",
		"{{.State.Running}}|{{.State.OOMKilled}}|{{.State.ExitCode}}|{{.State.Status}}", name).CombinedOutput()
	return classifyInspect(name, out, err)
}

// errInspectOutput marks docker inspect output the sampler cannot parse.
var errInspectOutput = errors.New("unexpected docker inspect output")

func classifyInspect(name string, out []byte, err error) (ContainerFailure, bool, error) {
	e := ContainerFailure{Name: name, Exited: true, Status: "removed", ExitCode: -1}
	text := strings.TrimSpace(string(out))
	if err != nil {
		// The only error that means the container is gone.
		if strings.Contains(text, "No such object") || strings.Contains(text, "No such container") {
			return e, false, nil
		}
		if text != "" {
			return e, false, fmt.Errorf("%w: %s", err, text)
		}
		return e, false, err
	}
	f := strings.Split(text, "|")
	if len(f) != 4 {
		return e, false, fmt.Errorf("%w for %s: %q", errInspectOutput, name, text)
	}
	if f[0] == "true" {
		return e, true, nil
	}
	e.OOMKilled = f[1] == "true"
	code, cerr := strconv.Atoi(f[2])
	if cerr != nil {
		return e, false, fmt.Errorf("%w for %s: exit code %q", errInspectOutput, name, f[2])
	}
	e.ExitCode = code
	e.Status = f[3]
	return e, false, nil
}

// CaptureLogs writes the last tail lines of a container's log (stdout and stderr,
// with timestamps) to path. Called when a container fails, because teardown
// removes the container and with it the only record of why it died.
func CaptureLogs(ctx context.Context, name, path string, tail int) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "logs", "--timestamps", "--tail", strconv.Itoa(tail), name).CombinedOutput()
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return mkErr
	}
	if writeErr := os.WriteFile(path, out, 0o644); writeErr != nil {
		return writeErr
	}
	return err
}

func (s *SystemSampler) sample(ctx context.Context) (SystemSample, []string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "stats", "--no-stream", "--format", "{{json .}}")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return SystemSample{}, nil, fmt.Errorf("docker stats timed out after 5s (daemon overloaded?)")
		}
		if e := strings.TrimSpace(stderr.String()); e != "" {
			return SystemSample{}, nil, fmt.Errorf("docker stats: %w: %s", err, e)
		}
		return SystemSample{}, nil, fmt.Errorf("docker stats: %w", err)
	}
	sample := SystemSample{T: time.Now()}
	var names []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		var d dockerStatsLine
		if jerr := json.Unmarshal([]byte(ln), &d); jerr != nil {
			s.mu.Lock()
			s.badLines++
			first := s.badLines == 1
			s.mu.Unlock()
			if first {
				s.log().Warn("unparseable docker stats line skipped (further ones logged at debug)", "line", ln, "err", jerr)
			} else {
				s.log().Debug("unparseable docker stats line skipped", "line", ln, "err", jerr)
			}
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
	return sample, names, nil
}

// match reports whether a container belongs to the platform under test. An empty
// filter matches nothing: matching everything would count unrelated containers
// (monitoring, other projects) and fail the run when one of them stops.
func (s *SystemSampler) match(name string) bool {
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
