package minidashboard

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

func parseMeminfo(data []byte) (float64, error) {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if key != "MemTotal" && key != "MemAvailable" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse %s: %w", key, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	total, haveTotal := values["MemTotal"]
	available, haveAvailable := values["MemAvailable"]
	if !haveTotal || !haveAvailable || total == 0 || available > total {
		return 0, fmt.Errorf("memory totals are unavailable")
	}
	return float64(total-available) * 100 / float64(total), nil
}
