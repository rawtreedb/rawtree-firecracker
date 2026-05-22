package observability

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"
)

func EmitFirecrackerNativeEvents(paths RuntimePaths, collector *Collector) error {
	if err := emitFirecrackerLogs(paths.FirecrackerLogPath, collector); err != nil {
		return err
	}
	return emitFirecrackerMetrics(paths.FirecrackerMetricsPath, collector)
}

func emitFirecrackerLogs(logPath string, collector *Collector) error {
	file, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	events := make([]Event, 0, 100)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		event := Event{
			"event_type": "sandbox.firecracker.vmm.log",
			"firecracker": map[string]any{
				"log": map[string]any{
					"line": line,
				},
			},
			"source": "firecracker_vmm_logger",
			"status": "success",
		}
		if sampledAt := sampledAtFromLogLine(line); sampledAt != "" {
			event["sampled_at"] = sampledAt
		}
		events = append(events, event)
		if len(events) < 100 {
			continue
		}
		if err := collector.RecordMany(events); err != nil {
			return err
		}
		events = events[:0]
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return collector.RecordMany(events)
}

func emitFirecrackerMetrics(metricsPath string, collector *Collector) error {
	file, err := os.Open(metricsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	events := make([]Event, 0, 100)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		metrics := map[string]any{}
		if err := json.Unmarshal([]byte(line), &metrics); err != nil {
			metrics = map[string]any{"raw_line": line}
		}

		event := Event{
			"event_type": "sandbox.firecracker.vmm.metrics",
			"firecracker": map[string]any{
				"metrics": metrics,
			},
			"source": "firecracker_vmm_metrics",
			"status": "success",
		}
		if sampledAt := sampledAtFromMetrics(metrics); sampledAt != "" {
			event["sampled_at"] = sampledAt
		}
		events = append(events, event)
		if len(events) < 100 {
			continue
		}
		if err := collector.RecordMany(events); err != nil {
			return err
		}
		events = events[:0]
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return collector.RecordMany(events)
}

func sampledAtFromMetrics(metrics map[string]any) string {
	value, ok := metrics["utc_timestamp_ms"]
	if !ok {
		return ""
	}

	var timestampMS int64
	switch typed := value.(type) {
	case float64:
		timestampMS = int64(typed)
	case int64:
		timestampMS = typed
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return ""
		}
		timestampMS = parsed
	default:
		return ""
	}

	return time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano)
}

var firecrackerLogTimestampPattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?`)

func sampledAtFromLogLine(line string) string {
	match := firecrackerLogTimestampPattern.FindStringSubmatch(line)
	if len(match) == 0 {
		return ""
	}

	fraction := match[2]
	if len(fraction) > 9 {
		fraction = fraction[:9]
	}
	for len(fraction) < 9 {
		fraction += "0"
	}

	parsed, err := time.Parse(time.RFC3339Nano, match[1]+"."+fraction+"Z")
	if err != nil {
		return ""
	}

	return parsed.UTC().Format(time.RFC3339Nano)
}
