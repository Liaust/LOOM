package hermesschedules

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// HelperBinding comes only from the immutable file linked into the installed
// helper. It is not accepted on stdin, from argv, or from the environment.
type HelperBinding struct {
	Source Source `json:"source"`
	Home   string `json:"home"`
	Caller string `json:"caller"`
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
	return binding, nil
}

func runFixedBinding(ctx context.Context, binding HelperBinding, out io.Writer) error {
	observer := Observer{Source: binding.Source, Read: FileReader(binding.Home)}
	observation, _ := observer.Observe(ctx)
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
		if !identifier.MatchString(job.ID) || seen[job.ID] {
			return Observation{}, ErrMalformed
		}
		if job.Project != nil && (!projectID.MatchString(job.Project.ProjectID) || !projectKey.MatchString(job.Project.Resource) || job.Project.Profile != source.Profile) {
			return Observation{}, ErrMalformed
		}
		seen[job.ID] = true
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
