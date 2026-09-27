package projectwatch

import (
	"encoding/json"
	"loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	sr "loom.local/loom/internal/serviceregistry"
	"testing"
)

func TestApplicationDataCustodyBinding(t *testing.T) {
	yes, no := true, false
	ref := pc.KnowledgeApplicationData{Application: "app", Data: "files", Subpath: "papers"}
	f := sr.ApplicationPrerequisiteSnapshot{SchemaVersion: sr.ApplicationPrerequisiteSchema, Owner: sr.ApplicationOwner{ProjectID: "project", NodeID: "node", Resource: "app"}, LocationRevision: "location", PolicyRevision: "policy", Installation: sr.ApplicationPrerequisiteInstallation{Present: true, Committed: &yes, Applied: &yes, Retired: &no, Fenced: &no, Revision: "installed"}, Data: map[string]sr.ApplicationPrerequisiteData{"files": {Path: "/data/app", BindingRef: "app.files", Availability: "available", Custody: "matches", PoolIdentity: "pool", Identity: &sr.ApplicationPrerequisiteDataIdentity{Device: 1, Inode: 2, PoolInode: 3, Mode: 0700}}}}
	for _, tc := range []string{"valid", "foreign", "fenced", "retired", "unclaimed", "missing", "binding"} {
		t.Run(tc, func(t *testing.T) {
			raw, _ := json.Marshal(f)
			var facts sr.ApplicationPrerequisiteSnapshot
			_ = json.Unmarshal(raw, &facts)
			data := facts.Data["files"]
			switch tc {
			case "foreign":
				facts.Owner.ProjectID = "other"
			case "fenced":
				facts.Installation.Fenced = &yes
			case "retired":
				facts.Installation.Retired = &yes
			case "unclaimed":
				data.Custody = "unclaimed"
			case "missing":
				data.Path = ""
			case "binding":
				data.BindingRef = "other"
			}
			facts.Data["files"] = data
			b, err := BindApplicationData(ref, "app.files", "project", "node", "location", facts)
			if (err == nil) != (tc == "valid") {
				t.Fatalf("binding=%+v error=%v", b, err)
			}
		})
	}
}

func TestApplicationDataPortableRootBinding(t *testing.T) {
	a, _ := declarationWatchControlFixture(t)
	item := a.Plan.WatchedRoots[0]
	item.Metadata["knowledge_source"].(map[string]any)["application_data"] = &pc.KnowledgeApplicationData{Application: "app", Data: "files", Subpath: "papers"}
	root, err := BindDeclarationRoot(a.Report.Project.ID, item)
	if err != nil {
		t.Fatal(err)
	}
	var cfg watchedroots.RootConfig
	_ = json.Unmarshal(root.ConfigJSON, &cfg)
	if cfg.RootRelativePath != "papers" || cfg.SafeRootKey != ApplicationDataSafeKey(a.Report.Project.ID, item.BackendRootKey) || root.CompilerConfigHash != item.ConfigHash {
		t.Fatalf("root: %+v", root)
	}
	if err := ValidateApplicationDataBinding(root); err == nil {
		t.Fatal("missing physical owner proof accepted")
	}
}
