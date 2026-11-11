package loomcli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/jobs"
)

type jobSummaryArtifact struct {
	Key        string `json:"key"`
	ArtifactID string `json:"artifact_id"`
	Read       string `json:"read_command,omitempty"`
}

type jobSummary struct {
	JobID             string               `json:"job_id"`
	Status            string               `json:"status"`
	AttemptCount      int                  `json:"attempt_count"`
	AutomaticLimit    int                  `json:"automatic_attempt_limit"`
	AboveLimit        bool                 `json:"above_automatic_attempt_limit"`
	ManualAction      bool                 `json:"manual_action_required"`
	ExitCode          *int                 `json:"exit_code,omitempty"`
	Outcome           string               `json:"agent_outcome,omitempty"`
	Question          string               `json:"question,omitempty"`
	ThreadID          string               `json:"thread_id,omitempty"`
	FailureCode       *string              `json:"failure_code,omitempty"`
	FailureMessage    string               `json:"failure_message,omitempty"`
	AttentionStatus   string               `json:"failure_attention_status,omitempty"`
	AttentionNote     string               `json:"failure_attention_note,omitempty"`
	Artifacts         []jobSummaryArtifact `json:"current_artifacts"`
	Truncated         []string             `json:"truncated_fields,omitempty"`
	UnverifiedResult  bool                 `json:"current_result_unverified,omitempty"`
	UnverifiedRefs    bool                 `json:"current_artifact_refs_unverified,omitempty"`
	RetainedAttempts  int                  `json:"retained_attempts"`
	RetainedOutputs   int                  `json:"retained_outputs"`
	RetainedArtifacts int                  `json:"retained_artifacts"`
	ArtifactsOmitted  int                  `json:"current_artifacts_omitted,omitempty"`
	Details           string               `json:"details_command,omitempty"`
	Logs              string               `json:"logs_command,omitempty"`
}

func summarizeJob(detail jobs.JobDetail) jobSummary {
	j := detail.Job
	s := jobSummary{JobID: j.JobID, Status: j.Status, AttemptCount: j.AttemptCount,
		AutomaticLimit: j.MaxAttempts, AboveLimit: j.MaxAttempts > 0 && j.AttemptCount > j.MaxAttempts,
		ManualAction: j.ManualAction, ExitCode: j.ExitCode, FailureCode: j.FailureCode,
		Artifacts: []jobSummaryArtifact{}, RetainedAttempts: len(detail.Attempts),
		RetainedOutputs: len(detail.Outputs), RetainedArtifacts: len(detail.Artifacts),
		Details: jobReadCommand("job inspect", j.JobID), Logs: jobReadCommand("job logs --stream all --tail 40", j.JobID)}
	bounded := func(value, field string) string {
		runes := []rune(strings.TrimSpace(value))
		if len(runes) > 512 {
			s.Truncated = append(s.Truncated, field)
			return string(runes[:509]) + "..."
		}
		return string(runes)
	}
	if j.FailureMessage != nil {
		s.FailureMessage = bounded(*j.FailureMessage, "failure_message")
	}
	s.AttentionStatus = bounded(j.FailureAttentionStatus, "failure_attention_status")
	s.AttentionNote = bounded(j.FailureAttentionNote, "failure_attention_note")
	// The job row is cleared on retry. Its output describes the current result;
	// retained detail.Outputs contains historical attempts and must not be folded in.
	var result struct {
		Outputs *struct {
			Outcome  string `json:"outcome"`
			Question string `json:"question"`
			ThreadID string `json:"thread_id"`
			Attempt  int    `json:"attempt"`
		} `json:"outputs"`
	}
	if len(j.OutputJSON) > 0 && json.Unmarshal(j.OutputJSON, &result) != nil {
		s.UnverifiedResult = true
	} else if result.Outputs != nil {
		if result.Outputs.Attempt != 0 && result.Outputs.Attempt != j.AttemptCount {
			s.UnverifiedResult = true
		} else {
			s.Outcome = bounded(result.Outputs.Outcome, "agent_outcome")
			s.Question = bounded(result.Outputs.Question, "question")
			s.ThreadID = bounded(result.Outputs.ThreadID, "thread_id")
		}
	}
	var refs []struct {
		Key string `json:"artifact_key"`
		ID  string `json:"artifact_id"`
	}
	if len(j.ArtifactRefsJSON) > 0 && json.Unmarshal(j.ArtifactRefsJSON, &refs) != nil {
		s.UnverifiedRefs = true
	} else {
		for _, ref := range refs {
			if len(s.Artifacts) == 8 {
				s.ArtifactsOmitted++
				continue
			}
			s.Artifacts = append(s.Artifacts, jobSummaryArtifact{Key: bounded(ref.Key, "artifact_key"),
				ArtifactID: bounded(ref.ID, "artifact_id"), Read: jobReadCommand("artifact read", ref.ID)})
		}
	}
	return s
}

func jobReadCommand(command, ref string) string {
	if ref == "" || !cliEntrySafeText(ref, 240) {
		return ""
	}
	return "loom " + command + " -- '" + strings.ReplaceAll(ref, "'", "'\\''") + "'"
}

func renderJobSummary(cmd *cobra.Command, s jobSummary) {
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\nStatus: %s\nAttempts: %d (automatic limit %d)\nManual action required: %t\n", s.JobID, s.Status, s.AttemptCount, s.AutomaticLimit, s.ManualAction)
	if s.AttentionStatus != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Failure attention: %s\n", s.AttentionStatus)
	}
	if s.AttentionNote != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Attention note: %q\n", s.AttentionNote)
	}
	if s.ManualAction && (s.AttentionStatus == "acknowledged" || s.AttentionStatus == "archived") {
		fmt.Fprintln(cmd.OutOrStdout(), "Attention is no longer active. The manual-action guard prevents automatic continuation/retry; it is not a new pending request.")
	}
	if s.AboveLimit {
		fmt.Fprintln(cmd.OutOrStdout(), "Attempt budget: exceeded; later attempts are not an automatic retry allowance.")
	}
	if s.ExitCode != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Process exit: %d\n", *s.ExitCode)
	}
	for _, item := range []struct{ label, value string }{{"Agent outcome", s.Outcome}, {"Question", s.Question}, {"Failure", s.FailureMessage}} {
		if item.value != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %q\n", item.label, item.value)
		}
	}
	if s.UnverifiedResult {
		fmt.Fprintln(cmd.OutOrStdout(), "Current agent result: unverified; inspect full detail.")
	}
	if s.UnverifiedRefs {
		fmt.Fprintln(cmd.OutOrStdout(), "Current artifact references: unverified; inspect full detail.")
	}
	for _, artifact := range s.Artifacts {
		fmt.Fprintf(cmd.OutOrStdout(), "Artifact %q: %s\n", artifact.Key, artifact.Read)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Full detail: %s\nLogs: %s\n", s.Details, s.Logs)
	if len(s.Truncated) > 0 || s.ArtifactsOmitted > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Some current-result detail is omitted; use the full-detail or artifact read above.")
	}
}
