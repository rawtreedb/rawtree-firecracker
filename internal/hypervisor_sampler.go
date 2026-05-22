package observability

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HypervisorSampler struct {
	collector *Collector
	interval  time.Duration
	pid       int
	stopCh    chan struct{}
	doneCh    chan struct{}
	mu        sync.Mutex
}

func StartHypervisorSampler(collector *Collector, pid int, interval time.Duration) *HypervisorSampler {
	sampler := &HypervisorSampler{
		collector: collector,
		interval:  interval,
		pid:       pid,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}

	go sampler.run()
	return sampler
}

func (s *HypervisorSampler) Stop() {
	close(s.stopCh)
	<-s.doneCh
}

func (s *HypervisorSampler) Sample() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitSample()
}

func (s *HypervisorSampler) run() {
	defer close(s.doneCh)
	s.Sample()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.Sample()
		}
	}
}

func (s *HypervisorSampler) emitSample() {
	sampledAt := time.Now().UTC().Format(time.RFC3339Nano)
	metrics, err := readHypervisorMetrics(s.pid)
	if err != nil {
		event := Event{
			"event_type": "sandbox.hypervisor.sample.failed",
			"source":     "host_hypervisor_sampler",
			"status":     "error",
		}
		for key, value := range ErrorFields(err) {
			event[key] = value
		}
		_ = s.collector.Record(event)
		return
	}

	_ = s.collector.Record(Event{
		"event_type": "sandbox.hypervisor.sample",
		"hypervisor": metrics,
		"sampled_at": sampledAt,
		"source":     "host_hypervisor_sampler",
		"status":     "success",
	})
}

func readHypervisorMetrics(pid int) (map[string]any, error) {
	stat, err := readProcStat(pid)
	if err != nil {
		return nil, err
	}

	status := readProcStatus(pid)
	io := readProcIO(pid)
	fdCount := readFDCount(pid)
	cgroup := readCgroupMetrics(pid)

	return map[string]any{
		"pid": pid,
		"process": map[string]any{
			"fd_count": fdCount,
			"io":       io,
			"stat":     stat,
			"status":   status,
		},
		"cgroup": cgroup,
	}, nil
}

func readProcStat(pid int) (map[string]any, error) {
	content, err := os.ReadFile(procPath(pid, "stat"))
	if err != nil {
		return nil, err
	}

	command, state, fields, err := parseProcStat(string(content))
	if err != nil {
		return nil, err
	}

	cpuUserTicks := numberFromString(field(fields, 11))
	cpuSystemTicks := numberFromString(field(fields, 12))
	rssPages := numberFromString(field(fields, 21))

	return map[string]any{
		"command":          command,
		"cpu_system_ticks": cpuSystemTicks,
		"cpu_total_ticks":  cpuUserTicks + cpuSystemTicks,
		"cpu_user_ticks":   cpuUserTicks,
		"major_faults":     numberFromString(field(fields, 9)),
		"minor_faults":     numberFromString(field(fields, 7)),
		"parent_pid":       numberFromString(field(fields, 1)),
		"rss_bytes":        rssPages * int64(os.Getpagesize()),
		"rss_pages":        rssPages,
		"start_time_ticks": numberFromString(field(fields, 19)),
		"state":            state,
		"threads":          numberFromString(field(fields, 17)),
		"vsize_bytes":      numberFromString(field(fields, 20)),
	}, nil
}

func parseProcStat(value string) (string, string, []string, error) {
	open := strings.Index(value, "(")
	close := strings.LastIndex(value, ")")
	if open == -1 || close == -1 || close <= open {
		return "", "", nil, os.ErrInvalid
	}

	command := value[open+1 : close]
	fields := strings.Fields(strings.TrimSpace(value[close+2:]))
	state := ""
	if len(fields) > 0 {
		state = fields[0]
	}
	return command, state, fields, nil
}

func readProcStatus(pid int) map[string]any {
	content, _ := os.ReadFile(procPath(pid, "status"))
	fields := parseColonKeyValueLines(string(content))

	return map[string]any{
		"nonvoluntary_context_switches": numberFromString(fields["nonvoluntary_ctxt_switches"]),
		"threads":                       numberFromString(fields["Threads"]),
		"vm_hwm_bytes":                  kibibytesToBytes(fields["VmHWM"]),
		"vm_peak_bytes":                 kibibytesToBytes(fields["VmPeak"]),
		"vm_rss_bytes":                  kibibytesToBytes(fields["VmRSS"]),
		"vm_size_bytes":                 kibibytesToBytes(fields["VmSize"]),
		"voluntary_context_switches":    numberFromString(fields["voluntary_ctxt_switches"]),
	}
}

func readProcIO(pid int) map[string]any {
	content, _ := os.ReadFile(procPath(pid, "io"))
	fields := parseColonKeyValueLines(string(content))

	return map[string]any{
		"cancelled_write_bytes": numberFromString(fields["cancelled_write_bytes"]),
		"read_bytes":            numberFromString(fields["read_bytes"]),
		"read_chars":            numberFromString(fields["rchar"]),
		"sys_read_count":        numberFromString(fields["syscr"]),
		"sys_write_count":       numberFromString(fields["syscw"]),
		"write_bytes":           numberFromString(fields["write_bytes"]),
		"write_chars":           numberFromString(fields["wchar"]),
	}
}

func readFDCount(pid int) int {
	entries, err := os.ReadDir(procPath(pid, "fd"))
	if err != nil {
		return 0
	}
	return len(entries)
}

func readCgroupMetrics(pid int) map[string]any {
	cgroupPath := readUnifiedCgroupPath(pid)
	if cgroupPath == "" {
		return map[string]any{}
	}

	cgroupRoot := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(cgroupPath, "/"))
	return map[string]any{
		"cpu_stat":             readCgroupCPUStat(cgroupRoot),
		"memory_current_bytes": readNumberFile(filepath.Join(cgroupRoot, "memory.current")),
		"memory_peak_bytes":    readNumberFile(filepath.Join(cgroupRoot, "memory.peak")),
		"path":                 cgroupPath,
	}
}

func readUnifiedCgroupPath(pid int) string {
	content, _ := os.ReadFile(procPath(pid, "cgroup"))
	for _, line := range strings.Split(string(content), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" {
			return parts[2]
		}
	}
	return ""
}

func readCgroupCPUStat(cgroupRoot string) map[string]any {
	content, _ := os.ReadFile(filepath.Join(cgroupRoot, "cpu.stat"))
	fields := parseSpaceKeyValueLines(string(content))

	return map[string]any{
		"nr_periods":     numberFromString(fields["nr_periods"]),
		"nr_throttled":   numberFromString(fields["nr_throttled"]),
		"system_usec":    numberFromString(fields["system_usec"]),
		"throttled_usec": numberFromString(fields["throttled_usec"]),
		"usage_usec":     numberFromString(fields["usage_usec"]),
		"user_usec":      numberFromString(fields["user_usec"]),
	}
}

func readNumberFile(filePath string) int64 {
	content, _ := os.ReadFile(filePath)
	return numberFromString(strings.TrimSpace(string(content)))
}

func parseColonKeyValueLines(content string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		separator := strings.Index(line, ":")
		if separator == -1 {
			continue
		}
		result[strings.TrimSpace(line[:separator])] = strings.TrimSpace(line[separator+1:])
	}
	return result
}

func parseSpaceKeyValueLines(content string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			result[fields[0]] = fields[1]
		}
	}
	return result
}

func procPath(pid int, name string) string {
	return filepath.Join("/proc", strconv.Itoa(pid), name)
}

func field(fields []string, index int) string {
	if index < 0 || index >= len(fields) {
		return ""
	}
	return fields[index]
}

func kibibytesToBytes(value string) int64 {
	return numberFromString(value) * 1024
}

var numberPattern = regexp.MustCompile(`-?\d+`)

func numberFromString(value string) int64 {
	match := numberPattern.FindString(value)
	if match == "" {
		return 0
	}

	parsed, err := strconv.ParseInt(match, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
