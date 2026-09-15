package serviceregistry

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"

	pc "loom.local/loom/internal/projectcontracts"
)

type ApplicationCredentialGenerator interface {
	CheckApplicationCredentialGeneration(context.Context, ApplicationOwner, string) error
	CreateApplicationCredential(context.Context, ApplicationOwner, string, string) (string, error)
	FindApplicationCredential(context.Context, ApplicationOwner, string, string) (string, error)
}

// Only references and a correlation title are persisted. Proton owns the value.
type applicationGeneratedCredential struct {
	Owner  ApplicationOwner `json:"owner"`
	Ref    string           `json:"ref"`
	Source string           `json:"source"`
	Nonce  string           `json:"nonce"`
	ItemID string           `json:"item_id,omitempty"`
}

func (s applicationGeneratedCredential) title() string {
	return "LOOM " + s.Owner.Instance() + " " + applicationCredentialName(s.Ref) + " " + s.Nonce
}

// The caller holds the application lock. Intent precedes the external create;
// an uncertain create is looked up, never automatically repeated.
func (r ApplicationRuntime) generatedApplicationCredential(ctx context.Context, owner ApplicationOwner, ref, source string) (string, error) {
	share, generate, ok := pc.ApplicationCredentialSourceShare(source)
	if !ok || !generate {
		return "", applicationError("credential.source_invalid")
	}
	g, ok := r.Credentials.(ApplicationCredentialGenerator)
	if !ok {
		return "", applicationError("credential.generator_unavailable")
	}
	key := "credential-" + owner.Instance() + "-" + applicationCredentialName(ref)
	var state applicationGeneratedCredential
	err := r.Store.read(key, &state)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return "", applicationError("credential.generation_state_invalid")
	}
	if fresh {
		if err = g.CheckApplicationCredentialGeneration(ctx, owner, share); err != nil {
			return "", err
		}
		state = applicationGeneratedCredential{Owner: owner, Ref: ref, Source: source, Nonce: rand.Text()}
		if err = r.Store.write(key, state); err != nil {
			return "", err
		}
	} else if state.Owner != owner || state.Ref != ref || state.Source != source || len(state.Nonce) != 26 || strings.Trim(state.Nonce, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") != "" {
		return "", applicationError("credential.generation_state_invalid")
	}
	if state.ItemID == "" {
		if fresh {
			if err = r.fail("credential:generation_intent"); err != nil {
				return "", err
			}
			state.ItemID, err = g.CreateApplicationCredential(ctx, owner, share, state.title())
			if err == nil {
				err = r.fail("credential:generated")
			}
		} else {
			state.ItemID, err = g.FindApplicationCredential(ctx, owner, share, state.title())
		}
		if err != nil || state.ItemID == "" {
			return "", applicationError("credential.generation_pending")
		}
		if _, _, _, valid := pc.ParseApplicationProtonReference("pass://" + share + "/" + state.ItemID + "/password"); !valid {
			return "", applicationError("credential.generation_pending")
		}
		if err = r.Store.write(key, state); err != nil {
			return "", err
		}
	}
	resolved := "pass://" + share + "/" + state.ItemID + "/password"
	if _, _, _, valid := pc.ParseApplicationProtonReference(resolved); !valid {
		return "", applicationError("credential.generation_state_invalid")
	}
	return resolved, nil
}
