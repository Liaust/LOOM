package projectapply

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strings"
	"time"

	pc "loom.local/loom/internal/projectcontracts"
)

// CanonicalJSON implements D0: recursive byte-sorted object keys, Go JSON
// escaping and exact integer decimals. Rejects floats, duplicates and trailing
// input rather than allowing a precision-losing decode/re-encode.
func CanonicalJSON(raw []byte) ([]byte, error) {
	v, err := pc.DecodeDeclarationEvidenceJSON(raw)
	if err != nil {
		return nil, fail(pc.DeclarationInvalid, "invalid_json")
	}
	var check func(any) bool
	check = func(v any) bool {
		switch x := v.(type) {
		case json.Number:
			s := x.String()
			if strings.ContainsAny(s, ".eE") {
				return false
			}
		case map[string]any:
			for _, v := range x {
				if !check(v) {
					return false
				}
			}
		case []any:
			for _, v := range x {
				if !check(v) {
					return false
				}
			}
		}
		return true
	}
	if !check(v) {
		return nil, fail(pc.DeclarationInvalid, "non_integer_json")
	}
	return json.Marshal(v)
}
func hashBytes(b []byte) string { return "sha256:" + fmtHex(sha256.Sum256(b)) }
func fmtHex(b [32]byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, 64)
	for i, x := range b {
		out[2*i] = h[x>>4]
		out[2*i+1] = h[x&15]
	}
	return string(out)
}
func canonicalValue(v any) ([]byte, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, fail(pc.DeclarationInvalid, "invalid_json")
	}
	return CanonicalJSON(raw)
}
func PayloadHash(raw []byte) (string, error) {
	b, e := CanonicalJSON(raw)
	if e != nil {
		return "", e
	}
	return hashBytes(b), nil
}

// PlanID normalizes ordering without changing the caller's DTO. Validation of
// executable plans (including the complete payload) is performed by Plan.
func PlanID(b pc.DeclarationPlanBasis) (string, error) {
	b, err := orderedBasis(b)
	if err != nil {
		return "", err
	}
	raw, err := canonicalValue(b)
	if err != nil {
		return "", err
	}
	return hashBytes(raw), nil
}
func orderedBasis(b pc.DeclarationPlanBasis) (pc.DeclarationPlanBasis, error) {
	b.Sources = append([]pc.DeclarationSource{}, b.Sources...)
	sort.Slice(b.Sources, func(i, j int) bool { return b.Sources[i].Ref < b.Sources[j].Ref })
	b.Effects = append([]pc.DeclarationEffect{}, b.Effects...)
	sort.Slice(b.Effects, func(i, j int) bool { return b.Effects[i] < b.Effects[j] })
	input := append([]pc.DeclarationAction{}, b.Actions...)
	byID := map[string]bool{}
	for i, a := range input {
		if byID[a.ID] || a.ID == "" {
			return b, fail(pc.DeclarationInvalid, "action_identity")
		}
		byID[a.ID] = true
		a.DependsOn = append([]string{}, a.DependsOn...)
		sort.Strings(a.DependsOn)
		input[i] = a
		for j, d := range a.DependsOn {
			if d == a.ID || (j > 0 && d == a.DependsOn[j-1]) {
				return b, fail(pc.DeclarationInvalid, "action_dependencies")
			}
		}
	}
	for _, a := range input {
		for _, d := range a.DependsOn {
			if !byID[d] {
				return b, fail(pc.DeclarationInvalid, "action_dependencies")
			}
		}
	}
	sort.Slice(input, func(i, j int) bool {
		a, z := input[i], input[j]
		return string(a.Owner)+"/"+string(a.Resource)+"/"+string(a.Kind) < string(z.Owner)+"/"+string(z.Resource)+"/"+string(z.Kind)
	})
	b.Actions = []pc.DeclarationAction{}
	done := map[string]bool{}
	for len(b.Actions) < len(input) {
		found := false
		for _, a := range input {
			if done[a.ID] {
				continue
			}
			ready := true
			for _, d := range a.DependsOn {
				ready = ready && done[d]
			}
			if ready {
				b.Actions = append(b.Actions, a)
				done[a.ID] = true
				found = true
				break
			}
		}
		if !found {
			return b, fail(pc.DeclarationInvalid, "action_cycle")
		}
	}
	return b, nil
}

func (s *Service) Plan(ctx context.Context, p Principal, r pc.DeclarationPlanRequest) (pc.DeclarationPlan, error) {
	x, e := s.resolve(ctx, p, r)
	return x.Plan, e
}
func (s *Service) resolve(ctx context.Context, p Principal, r pc.DeclarationPlanRequest) (Resolution, error) {
	if e := s.config(false); e != nil {
		return Resolution{}, e
	}
	if e := validPrincipal(p); e != nil {
		return Resolution{}, e
	}
	r, e := planRequest(r)
	if e != nil {
		return Resolution{}, e
	}
	x, e := s.resolver.Resolve(ctx, p, r)
	if e != nil {
		return Resolution{}, safeError(e, pc.DeclarationTargetUnavailable, "resolve_failed")
	}
	if e = s.read(ctx, p, x.Plan.Basis.Target); e != nil {
		return Resolution{}, e
	}
	if e = selectorTarget(r, x.Plan.Basis.Target); e != nil {
		return Resolution{}, e
	}
	if e = s.validate(ctx, p, &x, r.Effects); e != nil {
		return Resolution{}, e
	}
	x.Plan.Readiness = readiness(x.Plan.PlanID)
	x.Plan.Errors = []pc.DeclarationError{}
	x.Plan.GeneratedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return x, nil
}
func prerequisites(b pc.DeclarationPlanBasis) Prerequisites {
	return Prerequisites{Target: b.Target, Sources: b.Sources, Bindings: b.Bindings, Revisions: b.Revisions}
}
func same(a, b any) bool {
	x, e := canonicalValue(a)
	y, f := canonicalValue(b)
	return e == nil && f == nil && string(x) == string(y)
}
func readiness(revision string) pc.DeclarationReadiness {
	unknown := pc.DeclarationFact{State: pc.DeclarationUnknown, Revision: revision}
	return pc.DeclarationReadiness{Desired: pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: revision}, Queued: unknown, Applied: unknown, Processing: unknown, Healthy: unknown, Protected: unknown, Verified: unknown}
}
