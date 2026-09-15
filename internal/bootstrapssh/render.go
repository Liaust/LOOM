package bootstrapssh

import (
	"fmt"
	"io"
	"strings"
)

func Render(w io.Writer, result Result) {
	fmt.Fprintln(w, "LOOM SSH bootstrap plan")
	if result.Status != "" {
		fmt.Fprintf(w, "Status: %s\n", result.Status)
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Target")
	fmt.Fprintf(w, "  host: %s\n", result.Plan.Target.Host)
	if result.Plan.Target.User != "" {
		fmt.Fprintf(w, "  user: %s\n", result.Plan.Target.User)
	}
	if result.Plan.Target.Port > 0 {
		fmt.Fprintf(w, "  port: %d\n", result.Plan.Target.Port)
	}
	fmt.Fprintf(w, "  os: %s\n", fallback(result.Plan.Facts.OS, "unknown"))
	fmt.Fprintf(w, "  arch: %s\n", fallback(result.Plan.Facts.Arch, "unknown"))
	fmt.Fprintf(w, "  home: %s\n", fallback(result.Plan.Facts.HomeDir, "unknown"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Node")
	fmt.Fprintf(w, "  key: %s\n", result.Plan.SetupPlan.Spec.NodeKey)
	fmt.Fprintf(w, "  kind: %s\n", result.Plan.SetupPlan.Spec.NodeKind)
	fmt.Fprintf(w, "  role: %s\n", result.Plan.SetupPlan.Spec.NodeRole)
	fmt.Fprintf(w, "  runtime: %s\n", result.Plan.SetupPlan.Spec.RuntimeClass)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Source")
	fmt.Fprintf(w, "  mode: %s\n", result.Plan.Source.Mode)
	if result.Plan.Source.LocalPath != "" {
		fmt.Fprintf(w, "  local: %s\n", result.Plan.Source.LocalPath)
	}
	if result.Plan.Source.GitURL != "" {
		fmt.Fprintf(w, "  git: %s\n", result.Plan.Source.GitURL)
	}
	fmt.Fprintf(w, "  remote: %s\n", result.Plan.Source.RemotePath)
	fmt.Fprintf(w, "  copied: %t\n", result.SourceCopied)
	if len(result.Plan.Source.Excludes) > 0 {
		fmt.Fprintf(w, "  excludes: %s\n", strings.Join(result.Plan.Source.Excludes, ", "))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Setup")
	fmt.Fprintf(w, "  install mode: %s\n", result.Plan.SetupPlan.Spec.InstallMode)
	fmt.Fprintf(w, "  service manager: %s\n", result.Plan.SetupPlan.Spec.ServiceManager)
	fmt.Fprintf(w, "  manifest: %s\n", result.Plan.SetupPlan.Paths.ManifestPath)
	fmt.Fprintf(w, "  remote spec: %s\n", firstNonEmpty(result.Plan.RemoteSetup.SpecPath, "-"))
	fmt.Fprintln(w, "  would generate setup spec: true")
	fmt.Fprintf(w, "  would run setup apply: %t\n", result.Plan.WouldRunRemoteSetup && result.DryRun)
	fmt.Fprintf(w, "  setup spec copied: %t\n", result.SetupSpecCopied)
	if result.RemoteSetupNote != "" {
		fmt.Fprintf(w, "  note: %s\n", result.RemoteSetupNote)
	}
	if result.Enrollment != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Enrollment")
		fmt.Fprintf(w, "  status: %s\n", result.Enrollment.Status)
		if result.Enrollment.EnrollmentRequestID != "" {
			fmt.Fprintf(w, "  request: %s\n", result.Enrollment.EnrollmentRequestID)
		}
		if result.Enrollment.NodeID != "" {
			fmt.Fprintf(w, "  node: %s\n", result.Enrollment.NodeID)
		}
		if result.Enrollment.NodeCredentialID != "" {
			fmt.Fprintf(w, "  credential: %s\n", result.Enrollment.NodeCredentialID)
		}
		if result.Enrollment.CredentialHint != "" {
			fmt.Fprintf(w, "  credential hint: %s\n", result.Enrollment.CredentialHint)
		}
		if result.Enrollment.HeartbeatID != "" {
			fmt.Fprintf(w, "  heartbeat: %s\n", result.Enrollment.HeartbeatID)
		}
		fmt.Fprintf(w, "  verified on main: %t\n", result.Enrollment.VerifiedOnMain)
		if result.Enrollment.FailureCode != "" {
			fmt.Fprintf(w, "  failure: %s\n", result.Enrollment.FailureCode)
		}
		if result.Enrollment.FailureMessage != "" {
			fmt.Fprintf(w, "  message: %s\n", RedactEnrollmentSecrets(result.Enrollment.FailureMessage))
		}
	}
	if len(result.Steps) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Remote Steps")
		for _, step := range result.Steps {
			fmt.Fprintf(w, "  %s %s\n", step.Status, step.ID)
			if step.Message != "" {
				fmt.Fprintf(w, "    %s\n", step.Message)
			}
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Warnings")
		for _, warning := range result.Warnings {
			if warning.Field != "" {
				fmt.Fprintf(w, "  %s: %s (%s)\n", warning.Code, warning.Message, warning.Field)
			} else {
				fmt.Fprintf(w, "  %s: %s\n", warning.Code, warning.Message)
			}
		}
	}
}

func fallback(value string, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
