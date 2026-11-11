package hermesschedules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// HelperBinding comes only from the immutable file linked into the installed
// helper. It is not accepted on stdin, from argv, or from the environment.
type HelperBinding struct {
	Source    Source `json:"source"`
	Home      string `json:"home"`
	Caller    string `json:"caller"`
	BoxesFile string `json:"boxes_file,omitempty"`
}

// RunFixedHelper emits one sanitized observation, including native failures.
// Exit failure is reserved for an invalid invocation/binding or output failure.
func RunFixedHelper(ctx context.Context, args []string, bindingPath string, out io.Writer) error {
	if len(args) != 0 || !filepath.IsAbs(bindingPath) {
		return ErrSource
	}
	binding, err := loadBinding(bindingPath)
	if err != nil {
		return err
	}
	return runFixedBinding(ctx, binding, out)
}

// ServeFixedHelper is the no-argument socket-activated entry. Authenticate fd0
// before touching the native store. No bytes from the socket are interpreted.
func ServeFixedHelper(ctx context.Context, args []string, bindingPath string, in *os.File, out io.Writer) error {
	if len(args) != 0 || !filepath.IsAbs(bindingPath) {
		return ErrSource
	}
	binding, err := loadBinding(bindingPath)
	if err != nil {
		return err
	}
	if binding.Caller == "" || requireCallerPeer(int(in.Fd()), binding.Caller) != nil {
		return ErrSource
	}
	return runFixedBinding(ctx, binding, out)
}

func loadBinding(bindingPath string) (HelperBinding, error) {
	f, err := os.Open(bindingPath)
	if err != nil {
		return HelperBinding{}, ErrSource
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(raw) > 8192 {
		return HelperBinding{}, ErrSource
	}
	var binding HelperBinding
	if decodeStrict(raw, &binding) != nil || binding.Source.Host == "" || (binding.Source.Profile != "mina" && binding.Source.Profile != "morathustra") || !filepath.IsAbs(binding.Home) || filepath.Clean(binding.Home) != binding.Home || binding.Home == "/" {
		return HelperBinding{}, ErrSource
	}
	if binding.BoxesFile != "" && (!filepath.IsAbs(binding.BoxesFile) || filepath.Clean(binding.BoxesFile) != binding.BoxesFile) {
		return HelperBinding{}, ErrSource
	}
	return binding, nil
}

func runFixedBinding(ctx context.Context, binding HelperBinding, out io.Writer) error {
	observer := Observer{Source: binding.Source, Read: FileReader(binding.Home)}
	observation, _ := observer.Observe(ctx)
	if binding.Source.Revision == PlatformRevision && observation.Availability == Available {
		if err := appendProfileObservations(ctx, binding, &observation); err != nil {
			observation.Availability, observation.Jobs, observation.ObservedAt, observation.StoreUpdatedAt = Unavailable, nil, nil, nil
		}
	}
	// Buffer before writing so oversized projections cannot expose a partial
	// inventory. Native input never leaves this process.
	buf := &boundedOutput{limit: MaxBytes}
	if json.NewEncoder(buf).Encode(observation) != nil {
		return ErrTooLarge
	}
	n, err := out.Write(buf.Bytes())
	if err == nil && n != buf.Len() {
		return io.ErrShortWrite
	}
	return err
}

var declaredProfile = regexp.MustCompile(`^loom-[0-9a-f]{32}$`)

func appendProfileObservations(ctx context.Context, binding HelperBinding, out *Observation) error {
	root := filepath.Join(binding.Home, "profiles")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || len(entries) > 256 {
		return ErrUnavailable
	}
	if info, err := os.Lstat(root); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnavailable
	}
	for _, entry := range entries {
		if !declaredProfile.MatchString(entry.Name()) && !projectKey.MatchString(entry.Name()) {
			continue
		}
		home := filepath.Join(root, entry.Name())
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return ErrMalformed
		}
		var boxOwner *BoxOwnership
		boxMarker := filepath.Join(home, "loom-box-owner.json")
		if info, err := os.Lstat(boxMarker); err == nil {
			if !info.Mode().IsRegular() || info.Size() > 65536 {
				return ErrMalformed
			}
			raw, readErr := os.ReadFile(boxMarker)
			var value struct {
				BoxOwnership
				SchemaVersion string `json:"schema_version"`
				BoxKey        string `json:"box_key"`
			}
			if readErr != nil || len(raw) > 65536 || json.Unmarshal(raw, &value) != nil || value.SchemaVersion != "loom.box_agent.v1" || !validBoxOwnership(&value.BoxOwnership, entry.Name()) {
				return ErrMalformed
			}
			if validateBoxCatalog(binding.BoxesFile, value.BoxKey, &value.BoxOwnership) != nil {
				return ErrUnavailable
			}
			boxOwner = &value.BoxOwnership
		} else if !os.IsNotExist(err) {
			return ErrUnavailable
		}
		if boxOwner == nil && !declaredProfile.MatchString(entry.Name()) {
			continue
		}
		// Only profiles carrying a project owner marker are part of this view.
		markerPath := filepath.Join(home, "loom-project-owner.json")
		info, err := os.Lstat(markerPath)
		if os.IsNotExist(err) && boxOwner == nil {
			continue
		}
		if boxOwner != nil && err == nil {
			return ErrMalformed
		}
		if boxOwner == nil {
			if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
				return ErrUnavailable
			}
			raw, err := os.ReadFile(markerPath)
			var owner struct {
				ProjectID string `json:"project_id"`
				Resource  string `json:"resource"`
			}
			if err != nil || json.Unmarshal(raw, &owner) != nil || !projectID.MatchString(owner.ProjectID) || !projectKey.MatchString(owner.Resource) {
				return ErrMalformed
			}
			sum := sha256.Sum256([]byte(owner.ProjectID + "/" + owner.Resource))
			if entry.Name() != fmt.Sprintf("loom-%x", sum[:16]) {
				return ErrMalformed
			}
		}
		source := binding.Source
		source.Profile = entry.Name()
		reader := FileReader(home)
		if _, err := os.Stat(filepath.Join(home, "cron", "jobs.json")); os.IsNotExist(err) {
			continue
		}
		local, err := (Observer{Source: source, Read: reader}).Observe(ctx)
		if err != nil {
			return err
		}
		if boxOwner != nil {
			for i := range local.Jobs {
				b := local.Jobs[i].Box
				if b != nil && (b.BoxID != boxOwner.BoxID || b.Profile != boxOwner.Profile || b.Workdir != boxOwner.Workdir || b.ContextRevision != boxOwner.ContextRevision) {
					return ErrMalformed
				}
				if b == nil {
					value := *boxOwner
					b = &value
				}
				b.Retired = b.Retired || boxOwner.Retired
				local.Jobs[i].Box = b
			}
		}
		out.Jobs = append(out.Jobs, local.Jobs...)
	}
	return nil
}

func decodeStrict(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return ErrMalformed
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return ErrMalformed
	}
	return nil
}

// decodeObservation is an allowlisted IPC contract, never a generic JSON passthrough.
func decodeObservation(raw []byte, source Source) (Observation, error) {
	var out Observation
	if decodeStrict(raw, &out) != nil || out.Owner != "hermes" || out.Source != source || out.AttemptedAt.IsZero() {
		return Observation{}, ErrMalformed
	}
	switch out.Availability {
	case Unavailable, Malformed, Unsupported:
		if out.Jobs != nil || out.ObservedAt != nil || out.StoreUpdatedAt != nil {
			return Observation{}, ErrMalformed
		}
		return out, nil
	case Available:
		if out.Jobs == nil || out.ObservedAt == nil || out.ObservedAt.Before(out.AttemptedAt) {
			return Observation{}, ErrMalformed
		}
	default:
		return Observation{}, ErrMalformed
	}
	seen := map[string]bool{}
	for _, job := range out.Jobs {
		profile := job.Profile
		if profile == "" {
			profile = source.Profile
		}
		if profile != source.Profile && (source.Revision != PlatformRevision || (!declaredProfile.MatchString(profile) && !validBoxOwnership(job.Box, profile))) {
			return Observation{}, ErrMalformed
		}
		if job.Box != nil && !validBoxOwnership(job.Box, profile) {
			return Observation{}, ErrMalformed
		}
		identity := profile + "/" + job.ID
		if !identifier.MatchString(job.ID) || seen[identity] {
			return Observation{}, ErrMalformed
		}
		if job.Project != nil && (!projectID.MatchString(job.Project.ProjectID) || !projectKey.MatchString(job.Project.Resource) || job.Project.Profile != profile) {
			return Observation{}, ErrMalformed
		}
		seen[identity] = true
		switch job.NativeState {
		case StateUnknown, Scheduled, Paused, Completed, StateError:
		default:
			return Observation{}, ErrMalformed
		}
		switch job.LastResult {
		case ResultUnknown, OK, Error, BlockedConfig:
		default:
			return Observation{}, ErrMalformed
		}
		if job.Schedule.Timezone != nil || (job.DeliveryErrorReported != nil && !*job.DeliveryErrorReported) {
			return Observation{}, ErrMalformed
		}
		s := job.Schedule
		switch s.Kind {
		case Cron:
			if s.Minutes != nil || s.RunAt != nil || (s.Expression != nil && !cronText.MatchString(*s.Expression)) {
				return Observation{}, ErrMalformed
			}
		case Interval:
			if s.Expression != nil || s.RunAt != nil || (s.Minutes != nil && *s.Minutes <= 0) {
				return Observation{}, ErrMalformed
			}
		case Once:
			if s.Expression != nil || s.Minutes != nil {
				return Observation{}, ErrMalformed
			}
		case ScheduleUnknown:
			if s.Expression != nil || s.Minutes != nil || s.RunAt != nil {
				return Observation{}, ErrMalformed
			}
		default:
			return Observation{}, ErrMalformed
		}
	}
	return out, nil
}
