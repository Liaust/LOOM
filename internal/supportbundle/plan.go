package supportbundle

type CollectionPlan struct {
	SchemaVersion string             `json:"schema_version"`
	Profile       Profile            `json:"profile"`
	Limits        Limits             `json:"limits"`
	Privacy       PrivacyFlags       `json:"privacy"`
	Collectors    []PlannedCollector `json:"collectors"`
	Skipped       []PlannedCollector `json:"skipped,omitempty"`
}

type PlannedCollector struct {
	Key          string `json:"key"`
	Title        string `json:"title,omitempty"`
	PrivacyClass string `json:"privacy_class"`
	Reason       string `json:"reason,omitempty"`
}

func BuildPlan(input Options, collectors []Collector) (CollectionPlan, error) {
	opts, err := NormalizeOptions(input)
	if err != nil {
		return CollectionPlan{}, err
	}
	if collectors == nil {
		collectors = DefaultCollectors()
	}
	plan := CollectionPlan{
		SchemaVersion: SchemaVersion,
		Profile:       opts.Profile,
		Limits:        limitsFromOptions(opts),
		Privacy:       privacyFlagsFromOptions(opts),
	}
	for _, collector := range collectors {
		item := PlannedCollector{
			Key:          collector.Key,
			Title:        collector.Title,
			PrivacyClass: firstNonEmpty(collector.PrivacyClass, PrivacyDiagnosticSummary),
		}
		switch {
		case !profileAllows(opts.Profile, collector.Profiles):
			item.Reason = "profile_not_selected"
			plan.Skipped = append(plan.Skipped, item)
		case collector.RequiresLogs && !opts.IncludeLogs:
			item.Reason = "logs_not_requested"
			plan.Skipped = append(plan.Skipped, item)
		case collector.RequiresLive && !opts.IncludeLive:
			item.Reason = "live_probes_not_requested"
			plan.Skipped = append(plan.Skipped, item)
		default:
			plan.Collectors = append(plan.Collectors, item)
		}
	}
	return plan, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
