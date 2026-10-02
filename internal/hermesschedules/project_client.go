package hermesschedules

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"regexp"
	"time"
)

const ProjectTimeout = 15 * time.Second

var projectDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var projectKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var projectID = regexp.MustCompile(`^project_[A-Za-z0-9_-]+$`)

func ProjectSocketPath(profile string) string {
	if SocketPath(profile) == "" {
		return ""
	}
	return "/run/loom-" + profile + "-schedules/project.sock"
}

// ProjectClient never falls back to direct file access or another profile.
type ProjectClient struct{ Source Source }

func (c ProjectClient) Call(ctx context.Context, req ProjectRequest) (ProjectResponse, error) {
	if c.Source.Revision != NativeRevision || ProjectSocketPath(c.Source.Profile) == "" || c.Source.Host == "" || req.Source != c.Source {
		return ProjectResponse{}, ErrSource
	}
	ctx, cancel := context.WithTimeout(ctx, ProjectTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", ProjectSocketPath(c.Source.Profile))
	if err != nil {
		return ProjectResponse{}, ErrUnavailable
	}
	defer conn.Close()
	if requireActivatorPeer(conn) != nil {
		return ProjectResponse{}, ErrUnavailable
	}
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return ProjectResponse{}, ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > 65536 {
		return ProjectResponse{}, ErrTooLarge
	}
	if _, err = conn.Write(append(raw, '\n')); err != nil {
		return ProjectResponse{}, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(conn, MaxBytes+1))
	if err != nil {
		return ProjectResponse{}, ErrUnavailable
	}
	if int64(len(raw)) > MaxBytes {
		return ProjectResponse{}, ErrTooLarge
	}
	return decodeProjectResponse(raw, req)
}
func decodeProjectResponse(raw []byte, req ProjectRequest) (ProjectResponse, error) {
	var out ProjectResponse
	if decodeStrict(raw, &out) != nil || out.Source != req.Source {
		return out, ErrMalformed
	}
	if out.Error != "" {
		if out.Error == "conflict" {
			return out, ErrProjectConflict
		}
		if out.Error == "terminal_requires_native_resume" {
			return out, ErrTerminalRequiresResume
		}
		return out, ErrUnavailable
	}
	if out.Jobs == nil {
		return out, ErrMalformed
	}
	seen := map[string]bool{}
	for _, j := range out.Jobs {
		if !identifier.MatchString(j.ID) || !projectID.MatchString(j.ProjectID) || j.ProjectID != req.ProjectID || j.Profile != req.Source.Profile || !projectKey.MatchString(j.Resource) || !projectDigest.MatchString(j.Revision) || !projectDigest.MatchString(j.DesiredHash) || !projectDigest.MatchString(j.CommittedRevision) || !projectDigest.MatchString(j.InputHash) || (j.BeforeRevision != "absent" && !projectDigest.MatchString(j.BeforeRevision)) || (j.BeforeID != "" && !identifier.MatchString(j.BeforeID)) || j.Token == "" || seen[j.Resource] {
			return out, ErrMalformed
		}
		if req.Resource != "" && j.Resource != req.Resource {
			return out, ErrMalformed
		}
		seen[j.Resource] = true
	}
	return out, nil
}
