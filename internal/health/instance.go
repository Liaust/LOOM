package health

import (
	"strconv"
	"strings"
)

type InstanceIdentity struct {
	PID        int    `json:"pid"`
	StartTicks string `json:"start_ticks"`
}

func parseInstance(stat []byte, pid int) *InstanceIdentity {
	s := strings.TrimSpace(string(stat))
	open, close := strings.Index(s, "("), strings.LastIndex(s, ")")
	if pid <= 0 || open < 1 || close <= open || strings.TrimSpace(s[:open]) != strconv.Itoa(pid) {
		return nil
	}
	// comm may contain spaces and parentheses; the suffix begins at field 3.
	fields := strings.Fields(s[close+1:])
	if len(fields) <= 19 {
		return nil
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return nil
	}
	return &InstanceIdentity{PID: pid, StartTicks: strconv.FormatUint(ticks, 10)}
}
