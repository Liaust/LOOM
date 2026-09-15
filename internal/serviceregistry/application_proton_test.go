package serviceregistry

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestApplicationProtonNativeCommandsAndParsing(t *testing.T) {
	_, _, q := applicationProvisionFixture(t)
	for _, role := range []string{"Viewer", "Editor"} {
		var vectors [][]string
		p := applicationProtonResolver{CLI: "pass", SessionEnsure: "ensure", Run: func(_ context.Context, _ ApplicationOwner, program string, args []string, out io.Writer) error {
			if program == "ensure" {
				return nil
			}
			vectors = append(vectors, slices.Clone(args))
			switch strings.Join(args[:2], " ") {
			case "share list":
				_, _ = io.WriteString(out, `{"shares":[{"id":"share==","share_role":"`+role+`"}]}`)
			case "item create":
				_, _ = io.WriteString(out, "item==\n")
			case "item list":
				_, _ = io.WriteString(out, `{"items":[{"id":"item==","share_id":"share==","title":"exact-title","item_type":"login"}]}`)
			case "item view":
				_, _ = io.WriteString(out, " private-value \n\n")
			}
			return nil
		}}
		err := p.CheckApplicationCredentialGeneration(t.Context(), q.Owner, "share==")
		if (err == nil) != (role == "Editor") {
			t.Fatal("role mismatch", role, err)
		}
		if role == "Viewer" {
			continue
		}
		id, err := p.CreateApplicationCredential(t.Context(), q.Owner, "share==", "exact-title")
		if err != nil || id != "item==" {
			t.Fatal("create receipt", err)
		}
		id, err = p.FindApplicationCredential(t.Context(), q.Owner, "share==", "exact-title")
		if err != nil || id != "item==" {
			t.Fatal("reconcile", err)
		}
		value, err := p.ResolveApplicationCredential(t.Context(), q.Owner, "pass://share==/item==/password")
		if err != nil || string(value) != " private-value \n" {
			t.Fatal("field fidelity", err)
		}
		if !slices.Contains(vectors[1], "--generate-password=32,uppercase,symbols") {
			t.Fatal("native generation absent")
		}
		for _, args := range vectors {
			for _, arg := range args {
				if strings.Contains(arg, "private-value") || arg == "--show-secrets" || arg == "--password" {
					t.Fatal("unsafe command argument")
				}
			}
		}
	}
}

func TestApplicationProtonUncertainOrConflictingOutput(t *testing.T) {
	_, _, q := applicationProvisionFixture(t)
	for _, raw := range []string{`{"items":[]}`, `{"items":[{"id":"a","share_id":"wrong","title":"match","item_type":"login"}]}`, `{"items":[{"id":"a","share_id":"share","title":"match","item_type":"login"},{"id":"b","share_id":"share","title":"match","item_type":"login"}]}`, `not-json`} {
		p := applicationProtonResolver{CLI: "pass", SessionEnsure: "ensure", Run: func(_ context.Context, _ ApplicationOwner, program string, _ []string, out io.Writer) error {
			if program == "pass" {
				_, _ = io.WriteString(out, raw)
			}
			return nil
		}}
		if id, _ := p.FindApplicationCredential(t.Context(), q.Owner, "share", "match"); id != "" {
			t.Fatal("ambiguous item accepted")
		}
	}
	p := applicationProtonResolver{CLI: "pass", SessionEnsure: "ensure", Run: func(context.Context, ApplicationOwner, string, []string, io.Writer) error {
		return errors.New("private upstream error")
	}}
	if _, err := p.CreateApplicationCredential(t.Context(), q.Owner, "share", "title"); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("upstream error leaked", err)
	}
}
