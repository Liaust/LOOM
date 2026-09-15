// loom-hermes-recovery is the explicit, unscheduled native recovery adapter.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/hermesprofile"
)

func main() {
	if err := run(); err != nil && err != flag.ErrHelp {
		fmt.Fprintln(os.Stderr, "Hermes recovery refused:", err)
		os.Exit(1)
	}
}
func run() error { return runArgs(os.Args[1:], os.Stdout) }

func runArgs(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("loom-hermes-recovery", flag.ContinueOnError)
	flags.SetOutput(output)
	identity := flags.String("identity", "", "explicit producer identity: morathustra or mina")
	mode := flags.String("mode", "verify", "publish or verify")
	workspace := flags.String("workspace", "", "physical publishing workspace; original workspace for verification")
	pkg := flags.String("package", "", "exact retained or restored recovery package")
	binary := flags.String("hermes", "", "pinned Nix Hermes executable")
	id := flags.String("id", "", "stable recovery request ID")
	created := flags.String("created-at", "", "exact UTC request timestamp")
	keyFile := flags.String("signing-key-file", "", "externally provisioned owner-only Ed25519 private key")
	public := flags.String("public-key", "", "independently retained Ed25519 public key in hex")
	fixture := flags.Bool("fixture", false, "allow only a disposable .loom-acceptance workspace")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	selected := hermesprofile.RecoveryIdentity(*identity)
	origin, err := selected.WorkspaceRoot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	if *mode == "verify" {
		// Verification binds the requested original workspace, independently of
		// where the package is now located. Do not reinterpret a fixture path as
		// a production origin or infer the producer from its package directory.
		if *workspace != "" {
			if *identity != "" && *workspace != origin {
				return fmt.Errorf("verification workspace conflicts with selected identity")
			}
			origin = *workspace
		}
		key, err := hex.DecodeString(*public)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("verification public key required")
		}
		e, err := hermesprofile.Verify(ctx, *pkg, origin, key)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(e)
	}
	if *mode != "publish" {
		return fmt.Errorf("unknown mode")
	}
	location, origin, err := hermesprofile.ResolveWorkspace(selected, *workspace)
	if err != nil {
		return err
	}
	*workspace = location
	if *fixture {
		if !hermesprofile.FixtureWorkspace(*workspace) {
			return fmt.Errorf("production workspace is not a fixture")
		}
	} else if *workspace != origin || hermesprofile.FixtureWorkspace(*workspace) {
		return fmt.Errorf("noncanonical workspace is not an explicit fixture")
	}
	if !filepath.IsAbs(*keyFile) || strings.HasPrefix(*keyFile, *workspace+"/") {
		return fmt.Errorf("signing key must be outside workspace")
	}
	key, err := hermesprofile.ReadSigningKey(*keyFile)
	if err != nil {
		return err
	}
	defer clear(key)
	when, err := time.Parse(time.RFC3339Nano, *created)
	if err != nil || when.Location() != time.UTC || when.After(time.Now().UTC()) || time.Since(when) > hermesprofile.MaxAge {
		return fmt.Errorf("fresh UTC request timestamp required")
	}
	in := hermesprofile.PublishInput{Identity: selected, Workspace: *workspace, ID: *id, CreatedAt: when, PrivateKey: key, Binary: *binary}
	if *fixture {
		in.Runner = hermesprofile.RunFixtureBackup
	}
	e, err := hermesprofile.Publish(ctx, in)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(e)
}
