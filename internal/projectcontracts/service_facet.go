package projectcontracts

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const ServiceProvisioningExternal = "external"

// ResolveServiceRegistrationContract selects one explicitly requested contract.
// v0.5 does not enroll service files through the retired facet mechanism.
func ResolveServiceRegistrationContract(analysis Analysis, key string) (ServiceFacetItem, error) {
	if analysis.Loaded == nil || !analysis.Report.OK {
		return ServiceFacetItem{}, fmt.Errorf("project contract is not valid")
	}
	if analysis.Loaded.Contract.SchemaVersion != ProjectSchemaV05 {
		for _, item := range analysis.Plan.Services {
			if item.Key == key {
				return item, nil
			}
		}
		return ServiceFacetItem{}, fmt.Errorf("service contract %q was not found", key)
	}
	rel, err := KeyedProjectContractPath("services", key)
	if err != nil {
		return ServiceFacetItem{}, err
	}
	root, err := os.OpenRoot(analysis.Loaded.RootPath)
	if err != nil {
		return ServiceFacetItem{}, err
	}
	defer root.Close()
	raw, err := declarationReadSource(root, rel)
	if err != nil {
		return ServiceFacetItem{}, fmt.Errorf("read service contract %q: %w", key, err)
	}
	contract, err := ParseServiceRegistrationContract(raw)
	if err != nil {
		return ServiceFacetItem{}, err
	}
	expected, err := KeyedProjectContractPath("services", contract.Service.Key)
	if err != nil || rel != expected {
		return ServiceFacetItem{}, fmt.Errorf("service %q must be stored at %s", contract.Service.Key, expected)
	}
	if contract.Service.Class == ServiceClassSystem {
		return ServiceFacetItem{}, fmt.Errorf("project service declarations cannot use the system class")
	}
	providerKey := ProjectServiceProviderKey(analysis.Loaded.Contract.Project.Slug, contract.Service.Key)
	return ServiceFacetItem{
		Key: contract.Service.Key, ContractPath: rel, ContractHash: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)),
		ProviderKey: providerKey, ProviderAddress: contract.Service.TargetNode + "@" + providerKey,
		Provisioning: ServiceProvisioningExternal, ActivationStatus: "ready", Contract: contract,
	}, nil
}

func validateServiceFacet(loaded LoadedProject, project ProjectContract, add func(Diagnostic)) []ServiceFacetItem {
	if !project.Facets["services"] {
		return nil
	}
	dir := filepath.Join(loaded.RootPath, filepath.FromSlash(".loom/contracts/services"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			add(Diagnostic{Severity: SeverityError, Code: "services.read_failed", Message: err.Error(), File: ".loom/contracts/services"})
		}
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	items := make([]ServiceFacetItem, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if filepath.Ext(name) != ".yaml" {
			add(Diagnostic{Severity: SeverityError, Code: "services.contract_extension_invalid", Message: "service contracts must use the .yaml extension", File: filepath.ToSlash(filepath.Join(".loom/contracts/services", name))})
			continue
		}
		rel := filepath.ToSlash(filepath.Join(".loom/contracts/services", name))
		contract, raw, err := LoadServiceRegistrationContract(filepath.Join(dir, name))
		if err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "services.contract_invalid", Message: err.Error(), File: rel})
			continue
		}
		expected, err := KeyedProjectContractPath("services", contract.Service.Key)
		if err != nil || rel != expected {
			add(Diagnostic{Severity: SeverityError, Code: "services.contract_path_invalid", Message: fmt.Sprintf("service %q must be stored at %s", contract.Service.Key, expected), File: rel})
			continue
		}
		if contract.Service.Class == ServiceClassSystem {
			add(Diagnostic{Severity: SeverityError, Code: "services.system_class_forbidden", Message: "project service declarations cannot use the system class", File: rel, Field: "service.class"})
			continue
		}
		providerKey := ProjectServiceProviderKey(project.Project.Slug, contract.Service.Key)
		sum := sha256.Sum256(raw)
		items = append(items, ServiceFacetItem{
			Key: contract.Service.Key, ContractPath: rel, ContractHash: fmt.Sprintf("sha256:%x", sum[:]),
			ProviderKey: providerKey, ProviderAddress: contract.Service.TargetNode + "@" + providerKey,
			Provisioning: ServiceProvisioningExternal, ActivationStatus: "ready", Contract: contract,
		})
	}
	return items
}

// ProjectServiceProviderKey creates a stable provider key while preserving the
// existing node+provider-key registry uniqueness across projects.
func ProjectServiceProviderKey(projectSlug, serviceKey string) string {
	base := projectSlug + "-" + serviceKey
	if len(base) <= 63 && providerKeyPattern.MatchString(base) {
		return base
	}
	sum := sha256.Sum256([]byte(projectSlug + "\x00" + serviceKey))
	suffix := fmt.Sprintf("-%x", sum[:6])
	limit := 63 - len(suffix)
	prefix := strings.TrimRight(base[:limit], "_-")
	return prefix + suffix
}
