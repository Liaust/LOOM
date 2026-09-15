package portal

import (
	"net/url"
	"strings"
	"time"
)

const (
	mainHealthProbeTimeout       = 2 * time.Second
	maxAvailabilityDiagnosticLen = 240
)

type MainAvailabilityState string

const (
	MainAvailabilityOnline   MainAvailabilityState = "online"
	MainAvailabilityDegraded MainAvailabilityState = "degraded"
	MainAvailabilityOffline  MainAvailabilityState = "offline"
)

type MainTransportKind string

const (
	MainTransportUnknown MainTransportKind = "unknown"
	MainTransportHTTP    MainTransportKind = "http"
	MainTransportUnix    MainTransportKind = "unix"
)

type MainAvailability struct {
	State     MainAvailabilityState
	Transport MainTransportKind
	Target    string
	CheckedAt time.Time
	Source    string
	Summary   string
	Cause     string
}

func newMainAvailability(state MainAvailabilityState, options SnapshotOptions, checkedAt time.Time, source string, cause error) MainAvailability {
	summary := "Main is online."
	switch state {
	case MainAvailabilityDegraded:
		summary = "Main is reachable, but some portal data is unavailable."
	case MainAvailabilityOffline:
		summary = "Main node is unreachable."
	}
	return MainAvailability{
		State:     state,
		Transport: normalizeMainTransport(options.MainTransport),
		Target:    sanitizeMainTarget(options.MainTransport, options.MainTarget),
		CheckedAt: checkedAt.UTC(),
		Source:    strings.TrimSpace(source),
		Summary:   summary,
		Cause:     sanitizeAvailabilityCause(cause),
	}
}

func normalizeMainTransport(kind MainTransportKind) MainTransportKind {
	switch kind {
	case MainTransportHTTP, MainTransportUnix:
		return kind
	default:
		return MainTransportUnknown
	}
}

func sanitizeMainTarget(kind MainTransportKind, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if kind == MainTransportUnix {
		return boundedAvailabilityText(target)
	}
	parsed, err := url.Parse(target)
	if err != nil || (kind == MainTransportHTTP && (parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https"))) {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return boundedAvailabilityText(parsed.String())
}

func sanitizeAvailabilityCause(err error) string {
	if err == nil {
		return ""
	}
	fields := strings.Fields(err.Error())
	for index, field := range fields {
		candidate := strings.Trim(field, "\"'()[]{}<>,;")
		if !strings.Contains(candidate, "://") {
			continue
		}
		safe := sanitizeMainTarget(MainTransportHTTP, candidate)
		if safe == "" {
			safe = "[redacted target]"
		}
		fields[index] = strings.Replace(field, candidate, safe, 1)
	}
	return boundedAvailabilityText(strings.Join(fields, " "))
}

func boundedAvailabilityText(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxAvailabilityDiagnosticLen {
		return value
	}
	return strings.TrimSpace(string(runes[:maxAvailabilityDiagnosticLen-1])) + "…"
}
