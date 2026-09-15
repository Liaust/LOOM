package serviceregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	pc "loom.local/loom/internal/projectcontracts"
)

type applicationProtonResolver struct {
	CLI, SessionEnsure string
	Run                func(context.Context, ApplicationOwner, string, []string, io.Writer) error
}

func (p applicationProtonResolver) command(ctx context.Context, owner ApplicationOwner, args []string) ([]byte, error) {
	if p.Run == nil || !owner.valid() {
		return nil, applicationError("credential.resolver_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := p.Run(ctx, owner, p.SessionEnsure, nil, io.Discard); err != nil {
		return nil, applicationError("credential.session_unavailable")
	}
	var output applicationCredentialOutput
	if err := p.Run(ctx, owner, p.CLI, args, &output); err != nil {
		clear(output.data)
		return nil, applicationError("credential.proton_failed")
	}
	return output.data, nil
}

func (p applicationProtonResolver) ResolveApplicationCredential(ctx context.Context, owner ApplicationOwner, source string) ([]byte, error) {
	share, item, field, ok := pc.ParseApplicationProtonReference(source)
	if !ok {
		return nil, applicationError("credential.source_invalid")
	}
	output, err := p.command(ctx, owner, []string{"item", "view", "--share-id", share, "--item-id", item, "--field", field, "--output", "json"})
	defer clear(output)
	if err != nil {
		return nil, err
	}
	if !bytes.HasSuffix(output, []byte{'\n'}) || len(output) < 2 {
		return nil, applicationError("credential.value_invalid")
	}
	return bytes.Clone(output[:len(output)-1]), nil
}

func (p applicationProtonResolver) CheckApplicationCredentialGeneration(ctx context.Context, owner ApplicationOwner, share string) error {
	output, err := p.command(ctx, owner, []string{"share", "list", "--output", "json"})
	defer clear(output)
	if err != nil {
		return err
	}
	var result struct {
		Shares []struct {
			ID   string `json:"id"`
			Role string `json:"share_role"`
		} `json:"shares"`
	}
	if json.Unmarshal(output, &result) != nil {
		return applicationError("credential.share_state_invalid")
	}
	for _, s := range result.Shares {
		if s.ID == share && slices.Contains([]string{"Owner", "Admin", "Manager", "Editor"}, s.Role) {
			return nil
		}
	}
	return applicationError("credential.generation_requires_editor")
}

func (p applicationProtonResolver) CreateApplicationCredential(ctx context.Context, owner ApplicationOwner, share, title string) (string, error) {
	// Native generation keeps the new password out of argv, stdout and LOOM state.
	output, err := p.command(ctx, owner, []string{"item", "create", "login", "--share-id", share, "--title", title, "--generate-password=32,uppercase,symbols"})
	defer clear(output)
	if err != nil {
		return "", err
	}
	id := strings.TrimSuffix(string(output), "\n")
	if _, _, _, ok := pc.ParseApplicationProtonReference("pass://" + share + "/" + id + "/password"); !ok {
		return "", applicationError("credential.generation_pending")
	}
	return id, nil
}

func (p applicationProtonResolver) FindApplicationCredential(ctx context.Context, owner ApplicationOwner, share, title string) (string, error) {
	output, err := p.command(ctx, owner, []string{"item", "list", "--share-id", share, "--filter-type", "login", "--filter-state", "active", "--output", "json"})
	defer clear(output)
	if err != nil {
		return "", err
	}
	var result struct {
		Items []struct {
			ID      string `json:"id"`
			ShareID string `json:"share_id"`
			Title   string `json:"title"`
			Type    string `json:"item_type"`
		} `json:"items"`
	}
	if json.Unmarshal(output, &result) != nil {
		return "", applicationError("credential.generation_pending")
	}
	var found string
	for _, item := range result.Items {
		if item.Title != title {
			continue
		}
		if found != "" || item.ShareID != share || item.Type != "login" {
			return "", applicationError("credential.generation_pending")
		}
		if _, _, _, ok := pc.ParseApplicationProtonReference("pass://" + share + "/" + item.ID + "/password"); !ok {
			return "", applicationError("credential.generation_pending")
		}
		found = item.ID
	}
	return found, nil
}
