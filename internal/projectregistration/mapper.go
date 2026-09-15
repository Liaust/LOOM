// Package projectregistration maps accepted project-contract analysis into the
// persistence input shared by direct CLI and backend registration.
package projectregistration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

var sourceHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// BuildInput consumes only analysis evidence. In particular, it never reopens
// the project or repositories contracts after validation.
func BuildInput(analysis projectcontracts.Analysis, source string) (projects.RegisterProjectContractInput, error) {
	if analysis.Loaded == nil {
		return projects.RegisterProjectContractInput{}, fmt.Errorf("project contract was not loaded")
	}
	if d, err := projectcontracts.ParseProjectDeclaration(analysis.Loaded.Raw); err == nil && d.LegacyContracts != nil {
		return projects.RegisterProjectContractInput{}, fmt.Errorf("%s", projectcontracts.LegacyOwnerTransitionRequired)
	}
	if !analysis.Report.OK || !analysis.Report.Registerable || !analysis.Plan.Registerable {
		return projects.RegisterProjectContractInput{}, fmt.Errorf("project contract is not registerable")
	}
	return mapAnalyzedInput(analysis, source)
}

// BuildDeclarationIntent maps captured legacy-reference analysis to untrusted
// desired input. It grants no mutation authority: direct registration remains
// guarded and the declaration owner must authenticate its journal predecessor.
type DeclarationIntent struct {
	Input          projects.RegisterProjectContractInput
	LegacyContract json.RawMessage
}

func BuildDeclarationIntent(analysis projectcontracts.Analysis, source string) (DeclarationIntent, error) {
	if analysis.Loaded == nil || !analysis.Report.OK || analysis.Report.Registerable || analysis.Plan.Registerable {
		return DeclarationIntent{}, fmt.Errorf("legacy declaration intent evidence required")
	}
	d, err := projectcontracts.ParseProjectDeclaration(analysis.Loaded.Raw)
	if err != nil || d.LegacyContracts == nil {
		return DeclarationIntent{}, fmt.Errorf("legacy declaration source required")
	}
	input, err := mapAnalyzedInput(analysis, source)
	if err != nil {
		return DeclarationIntent{}, err
	}
	// Strict source shape/closure was checked by the existing D4d compiler.
	// Use its captured retained source and existing legacy normalizer so the
	// owner can compare the actual old registered contract, not only its hash.
	for _, snapshot := range analysis.Plan.Declaration.Sources {
		if snapshot.Ref == d.LegacyContracts.Project.Ref {
			var old projectcontracts.ProjectContract
			if err = yaml.Unmarshal(snapshot.Raw, &old); err != nil {
				return DeclarationIntent{}, err
			}
			raw, err := json.Marshal(projectcontracts.NormalizeContract(old))
			return DeclarationIntent{Input: input, LegacyContract: raw}, err
		}
	}
	return DeclarationIntent{}, fmt.Errorf("retained project source required")
}

func mapAnalyzedInput(analysis projectcontracts.Analysis, source string) (projects.RegisterProjectContractInput, error) {
	if analysis.Report.SchemaVersion != projectcontracts.ReportSchemaV03 || analysis.Plan.SchemaVersion != projectcontracts.PlanSchemaV03 {
		return projects.RegisterProjectContractInput{}, fmt.Errorf("project analysis schemas are not supported")
	}
	if analysis.Report.Project != analysis.Plan.Project {
		return projects.RegisterProjectContractInput{}, fmt.Errorf("validator report and registration plan project identity do not match")
	}

	contract := projectcontracts.NormalizeContract(analysis.Loaded.Contract)
	if contract.SchemaVersion == projectcontracts.ProjectSchemaV04 && strings.TrimSpace(analysis.Plan.Project.ID) == "" {
		return projects.RegisterProjectContractInput{}, fmt.Errorf("project.contract.v0.4 registration requires project.id")
	}
	if strings.TrimSpace(contract.Project.ID) != strings.TrimSpace(analysis.Plan.Project.ID) {
		return projects.RegisterProjectContractInput{}, fmt.Errorf("validated project contract and registration plan project IDs do not match")
	}

	repositorySource, err := mapRepositorySource(analysis)
	if err != nil {
		return projects.RegisterProjectContractInput{}, err
	}
	var contractSource any = contract
	if contract.SchemaVersion == projectcontracts.ProjectSchemaV05 {
		d, err := projectcontracts.ParseProjectDeclaration(analysis.Loaded.Raw)
		if err != nil {
			return projects.RegisterProjectContractInput{}, err
		}
		compilationEqual := reflect.DeepEqual(analysis.Report.Declaration, analysis.Plan.Declaration)
		if d.LegacyContracts != nil {
			// D4d's compiled imported filters can have empty omitempty slices.
			// Its plan clone preserves serialized intent, not those slice headers.
			compilationEqual = mapperEvidenceEqual(analysis.Report.Declaration, analysis.Plan.Declaration)
		}
		if analysis.Loaded.Declaration == nil || !reflect.DeepEqual(d, *analysis.Loaded.Declaration) || analysis.Report.Declaration == nil || analysis.Plan.Declaration == nil || !reflect.DeepEqual(d, analysis.Report.Declaration.Document) || !compilationEqual {
			return projects.RegisterProjectContractInput{}, fmt.Errorf("declaration source, report and plan disagree")
		}
		expected := projectcontracts.NormalizeContract(projectcontracts.ProjectContract{Project: d.Project}).Project
		if analysis.Report.Project != (projectcontracts.PlanProject{ID: expected.ID, Slug: expected.Slug, Name: expected.Name, Description: expected.Description, OwnerNode: expected.OwnerNode, Status: expected.Status}) {
			return projects.RegisterProjectContractInput{}, fmt.Errorf("declaration project identity differs from registration output")
		}
		rootRef, pathErr := filepath.Rel(analysis.Loaded.RootPath, analysis.Loaded.ContractPath)
		if pathErr != nil || analysis.Report.ProjectRoot != analysis.Loaded.RootPath || analysis.Plan.ProjectRoot != analysis.Loaded.RootPath || analysis.Report.ContractPath != analysis.Loaded.ContractPath || analysis.Plan.ContractPath != analysis.Loaded.ContractPath {
			return projects.RegisterProjectContractInput{}, fmt.Errorf("declaration source location differs from analysis")
		}
		expectedSources := map[string]string{filepath.ToSlash(rootRef): projectcontracts.ProjectSchemaV05}
		for _, resource := range d.Resources {
			if resource.Protection != nil {
				expectedSources[resource.Protection.PolicyRef] = projectcontracts.BackupPolicySchemaV03
			}
			if resource.Application != nil {
				if ref := resource.Application.ArtifactDescriptor; ref != "" {
					expectedSources[ref] = "application.artifact.v1"
				}
				if _, exists := expectedSources[resource.Application.Manifest]; !exists {
					expectedSources[resource.Application.Manifest] = "application.contract.v1"
				}
			}
		}
		if d.LegacyContracts != nil {
			legacy, err := projectcontracts.CompileDeclarationLegacyReferences(*analysis.Loaded, *analysis.Plan.Declaration)
			if err != nil || legacy == nil || !mapperEvidenceEqual(legacy, analysis.Plan.Declaration.LegacyContracts) {
				return projects.RegisterProjectContractInput{}, fmt.Errorf("legacy declaration source closure disagrees")
			}
			// The existing strict compiler owns allowed retained references and
			// singleton alternatives. The ordinary source allowlist stays closed.
			expectedSources = map[string]string{}
			for _, snapshot := range analysis.Plan.Declaration.Sources {
				expectedSources[snapshot.Ref] = snapshot.SchemaVersion
			}
		}
		seen := map[string]bool{}
		rootBound := false
		for _, snapshot := range analysis.Plan.Declaration.Sources {
			if seen[snapshot.Ref] {
				return projects.RegisterProjectContractInput{}, fmt.Errorf("duplicate declaration source snapshot")
			}
			seen[snapshot.Ref] = true
			expectedVersion, expected := expectedSources[snapshot.Ref]
			if !expected || snapshot.SchemaVersion != expectedVersion {
				return projects.RegisterProjectContractInput{}, fmt.Errorf("unexpected declaration source or schema mismatch")
			}
			hash := fmt.Sprintf("sha256:%x", sha256.Sum256(snapshot.Raw))
			if hash != snapshot.Hash || hash != snapshot.Revision {
				return projects.RegisterProjectContractInput{}, fmt.Errorf("declaration source snapshot hash mismatch")
			}
			if snapshot.Ref == filepath.ToSlash(rootRef) && snapshot.SchemaVersion == projectcontracts.ProjectSchemaV05 && bytes.Equal(snapshot.Raw, analysis.Loaded.Raw) {
				rootBound = true
			}
		}
		for ref := range expectedSources {
			if !seen[ref] {
				return projects.RegisterProjectContractInput{}, fmt.Errorf("missing declaration source snapshot: %s", ref)
			}
		}
		if !rootBound {
			return projects.RegisterProjectContractInput{}, fmt.Errorf("declaration root source is unbound")
		}
		if err := projectcontracts.ValidateDeclarationEnrollment(*analysis.Loaded, analysis.Report, analysis.Plan); err != nil {
			return projects.RegisterProjectContractInput{}, err
		}
		contractSource = d
	}
	contractJSON, err := json.Marshal(contractSource)
	if err != nil {
		return projects.RegisterProjectContractInput{}, err
	}
	reportJSON, err := json.Marshal(analysis.Report)
	if err != nil {
		return projects.RegisterProjectContractInput{}, err
	}
	planJSON, err := json.Marshal(analysis.Plan)
	if err != nil {
		return projects.RegisterProjectContractInput{}, err
	}
	providersJSON, err := json.Marshal(analysis.Plan.DerivedProviders)
	if err != nil {
		return projects.RegisterProjectContractInput{}, err
	}
	policyRefsJSON, err := json.Marshal(analysis.Plan.PolicyRefs)
	if err != nil {
		return projects.RegisterProjectContractInput{}, err
	}
	metadataJSON, err := json.Marshal(map[string]string{"source": strings.TrimSpace(source)})
	if err != nil {
		return projects.RegisterProjectContractInput{}, err
	}

	facets := make([]projects.ProjectContractFacetInput, 0, len(analysis.Plan.Facets))
	for _, facet := range analysis.Plan.Facets {
		facets = append(facets, projects.ProjectContractFacetInput{
			Key:         facet.Key,
			Folder:      facet.Folder,
			Enabled:     facet.Enabled,
			Present:     facet.Present,
			Placeholder: facet.Placeholder,
		})
	}
	projectContractHash := sha256.Sum256(analysis.Loaded.Raw)
	return projects.RegisterProjectContractInput{
		ProjectRoot:           analysis.Loaded.RootPath,
		ContractPath:          analysis.Loaded.ContractPath,
		ContractHash:          fmt.Sprintf("sha256:%x", projectContractHash[:]),
		ContractSchemaVersion: contract.SchemaVersion,
		Contract:              json.RawMessage(contractJSON),
		ValidationReport:      json.RawMessage(reportJSON),
		RegistrationPlan:      json.RawMessage(planJSON),
		Project: projects.ProjectContractProjectInput{
			ID:          analysis.Plan.Project.ID,
			Slug:        analysis.Plan.Project.Slug,
			Name:        analysis.Plan.Project.Name,
			Description: analysis.Plan.Project.Description,
			OwnerNode:   analysis.Plan.Project.OwnerNode,
			Status:      analysis.Plan.Project.Status,
		},
		RepositorySource: repositorySource,
		DerivedProviders: json.RawMessage(providersJSON),
		Facets:           facets,
		PolicyRefs:       json.RawMessage(policyRefsJSON),
		Metadata:         json.RawMessage(metadataJSON),
	}, nil
}

func mapperEvidenceEqual(a, b any) bool {
	ar, err := json.Marshal(a)
	if err != nil {
		return false
	}
	br, err := json.Marshal(b)
	if err != nil {
		return false
	}
	av, ae := projectcontracts.DecodeDeclarationEvidenceJSON(ar)
	bv, be := projectcontracts.DecodeDeclarationEvidenceJSON(br)
	return ae == nil && be == nil && reflect.DeepEqual(av, bv)
}

func mapRepositorySource(analysis projectcontracts.Analysis) (*projects.RegisterProjectRepositorySourceInput, error) {
	reportSource := analysis.Report.RepositorySource
	planSource := analysis.Plan.RepositorySource
	if reportSource == nil || planSource == nil {
		if reportSource != nil || planSource != nil {
			return nil, fmt.Errorf("validator report and registration plan repository source snapshots do not match")
		}
		if repositoryFacetDeclared(analysis.Report.Facets, analysis.Report.Repos, analysis.Report.RepositoryMembers) ||
			repositoryFacetDeclared(analysis.Plan.Facets, analysis.Plan.Repos, analysis.Plan.RepositoryMembers) {
			return nil, fmt.Errorf("repository source metadata is required for the validator-declared repository facet")
		}
		return nil, nil
	}
	if *reportSource != *planSource {
		return nil, fmt.Errorf("validator report and registration plan repository source snapshots do not match")
	}
	if err := validateRepositorySourceSnapshot(*reportSource); err != nil {
		return nil, err
	}
	return &projects.RegisterProjectRepositorySourceInput{
		ContractPath:          reportSource.ContractPath,
		ContractHash:          reportSource.ContractHash,
		ContractSchemaVersion: reportSource.ContractSchemaVersion,
	}, nil
}

func repositoryFacetDeclared(facets []projectcontracts.PlanFacet, repos []projectcontracts.RepoFacetItem, members []projectcontracts.RepoMemberSpec) bool {
	if len(repos) > 0 || len(members) > 0 {
		return true
	}
	for _, facet := range facets {
		if strings.EqualFold(strings.TrimSpace(facet.Key), "repos") && (facet.Enabled || facet.Present) {
			return true
		}
	}
	return false
}

func validateRepositorySourceSnapshot(source projectcontracts.RepositorySourceSnapshot) error {
	if source.ContractPath == "" || strings.TrimSpace(source.ContractPath) != source.ContractPath {
		return fmt.Errorf("validated repository source contract path is incomplete")
	}
	if !sourceHashPattern.MatchString(source.ContractHash) {
		return fmt.Errorf("validated repository source contract hash is invalid")
	}
	switch source.ContractSchemaVersion {
	case projectcontracts.ReposSchemaV03, projectcontracts.ReposSchemaV04:
		return nil
	default:
		return fmt.Errorf("validated repository source schema version is unsupported: %s", source.ContractSchemaVersion)
	}
}
