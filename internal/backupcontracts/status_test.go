package backupcontracts

import (
	"testing"
	"time"
)

func TestProjectProtectedFolderStatusPrecedence(t *testing.T) {
	tests := []struct {
		name string
		in   StatusEvidence
		want string
	}{
		{name: "disabled overrides report", in: StatusEvidence{ContractDisabled: true, RootReported: true, AcceptedBackup: true}, want: ProtectedFolderStatusDisabled},
		{name: "attention overrides activating", in: StatusEvidence{BlockingAttention: true, DesiredRegistered: true}, want: ProtectedFolderStatusAttention},
		{name: "offline preflight pending", in: StatusEvidence{PreflightPending: true, NodeUnavailable: true}, want: ProtectedFolderStatusWaitingForNode},
		{name: "online preflight pending", in: StatusEvidence{PreflightPending: true}, want: ProtectedFolderStatusActivating},
		{name: "offline reconcile pending", in: StatusEvidence{ReconcilePending: true, NodeUnavailable: true}, want: ProtectedFolderStatusWaitingForNode},
		{name: "ready", in: StatusEvidence{PreflightCompleted: true}, want: ProtectedFolderStatusReady},
		{name: "activating", in: StatusEvidence{DesiredRegistered: true}, want: ProtectedFolderStatusActivating},
		{name: "active", in: StatusEvidence{DesiredRegistered: true, NodeApplied: true, RootReported: true, ConfigHashMatches: true}, want: ProtectedFolderStatusActive},
		{name: "protected", in: StatusEvidence{DesiredRegistered: true, NodeApplied: true, RootReported: true, ConfigHashMatches: true, AcceptedBackup: true}, want: ProtectedFolderStatusProtected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ProjectProtectedFolderStatus(test.in); got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProjectRecordUsesOnlyCurrentGenerationEvidence(t *testing.T) {
	now := time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)
	beforeAck := now.Add(-time.Minute)
	afterAck := now.Add(time.Minute)
	contract := Contract{Key: "photos", OwnerNode: "macbook", Status: StatusActive, Target: TargetSpec{Path: "/Users/test/Photos"}}
	tests := []struct {
		name     string
		contract Contract
		evidence registrationEvidence
		want     string
	}{
		{name: "stale registration revision", contract: contract, evidence: registrationEvidence{ContractKey: "photos", Presence: "online", DesiredRevision: 2, AppliedRevision: 1, DesiredHash: "sha256:new", AppliedHash: "sha256:old"}, want: ProtectedFolderStatusActivating},
		{name: "offline owner waiting", contract: contract, evidence: registrationEvidence{ContractKey: "photos", Presence: "offline", DesiredRevision: 2, AppliedRevision: 1, DesiredHash: "sha256:new"}, want: ProtectedFolderStatusWaitingForNode},
		{name: "old root report mismatch", contract: contract, evidence: registrationEvidence{ContractKey: "photos", Presence: "online", DesiredRevision: 2, AppliedRevision: 2, DesiredHash: "sha256:new", AppliedHash: "sha256:new", RootID: "watched_root_photos", RootConfigHash: "sha256:old", AcceptedBackupAt: &now}, want: ProtectedFolderStatusAttention},
		{name: "blocking finding", contract: contract, evidence: registrationEvidence{ContractKey: "photos", Presence: "online", DesiredRevision: 2, AppliedRevision: 2, DesiredHash: "sha256:new", AppliedHash: "sha256:new", RootID: "watched_root_photos", RootConfigHash: "sha256:new", OpenFindingCount: 1}, want: ProtectedFolderStatusAttention},
		{name: "failed latest backup", contract: contract, evidence: registrationEvidence{ContractKey: "photos", Presence: "online", DesiredRevision: 2, AppliedRevision: 2, DesiredHash: "sha256:new", AppliedHash: "sha256:new", RootID: "watched_root_photos", RootConfigHash: "sha256:new", LatestBackupStatus: "failed"}, want: ProtectedFolderStatusAttention},
		{name: "accepted backup with stale report", contract: contract, evidence: registrationEvidence{ContractKey: "photos", Presence: "online", DesiredRevision: 2, AppliedRevision: 2, DesiredHash: "sha256:new", AppliedHash: "sha256:new", RootID: "watched_root_photos", RootConfigHash: "sha256:new", LastAckAt: &now, RootReportedAt: &beforeAck, AcceptedBackupAt: &now}, want: ProtectedFolderStatusActive},
		{name: "accepted backup with current report", contract: contract, evidence: registrationEvidence{ContractKey: "photos", Presence: "online", DesiredRevision: 2, AppliedRevision: 2, DesiredHash: "sha256:new", AppliedHash: "sha256:new", RootID: "watched_root_photos", RootConfigHash: "sha256:new", LastAckAt: &now, RootReportedAt: &afterAck, AcceptedBackupAt: &now}, want: ProtectedFolderStatusProtected},
		{name: "disabled overrides accepted backup", contract: Contract{Key: "photos", OwnerNode: "macbook", Status: StatusDisabled, Target: TargetSpec{Path: "/Users/test/Photos"}}, evidence: registrationEvidence{ContractKey: "photos", Presence: "online", DesiredRevision: 2, AppliedRevision: 2, DesiredHash: "sha256:new", AppliedHash: "sha256:new", RootID: "watched_root_photos", RootConfigHash: "sha256:new", AcceptedBackupAt: &now}, want: ProtectedFolderStatusDisabled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := projectRecord(test.contract, "/box/.loom/contracts/backup/photos.yaml", test.evidence, nil)
			if record.Lifecycle != test.want {
				t.Fatalf("lifecycle=%q want=%q record=%#v", record.Lifecycle, test.want, record)
			}
		})
	}
}

func TestIntentionalDeletionProjectionDistinguishesPendingConvergedAndMissingYAML(t *testing.T) {
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	pending := registrationEvidence{
		ContractKey: "photos", ContractPath: "/box/.loom/contracts/backup/photos.yaml", TargetPath: "/Users/test/Photos",
		NodeKey: "macbook", Presence: "offline", ActivationStatus: "stale", DesiredRevision: 3, AppliedRevision: 2, DeletedAt: &now,
	}
	if intentionalDeletionConverged(pending) {
		t.Fatal("queued deletion projected as converged")
	}
	record := intentionalDeletionRecord(pending)
	if !record.Deleting || record.MissingContract || record.Lifecycle != ProtectedFolderStatusWaitingForNode || record.Contract.Target.Path != pending.TargetPath {
		t.Fatalf("queued deletion projection = %#v", record)
	}
	completed := pending
	completed.ActivationStatus = "disabled"
	completed.AppliedRevision = completed.DesiredRevision
	completed.LastAckStatus = "completed"
	if !intentionalDeletionConverged(completed) {
		t.Fatalf("completed tombstone did not converge: %#v", completed)
	}
	accidental := pending
	accidental.DeletedAt = nil
	if intentionalDeletionConverged(accidental) {
		t.Fatal("accidental missing YAML was treated as intentional deletion")
	}
}
