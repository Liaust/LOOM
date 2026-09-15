package projectcontracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/scripts"
)

const (
	ScriptActivationStatusDisabled = "disabled"
	ScriptActivationStatusPending  = "pending_later_slice"
)

func validateScriptFacet(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) []ScriptFacetItem {
	if !contract.Facets["scripts"] {
		return nil
	}
	root := filepath.Join(loaded.RootPath, "scripts")
	entries, err := projectFacetEntries(loaded, "scripts")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		add(Diagnostic{Severity: SeverityError, Code: "script.facet_read_failed", Message: "could not read scripts folder: " + err.Error(), File: root, Field: "facets.scripts"})
		return nil
	}

	items := []ScriptFacetItem{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || ignoredScriptPackageDir(name) {
			continue
		}
		packageLoaded, packageRoot := projectFacetPackage(loaded, "scripts", name)
		item := validateScriptPackage(packageLoaded, contract, name, packageRoot, add)
		if item.Key != "" {
			items = append(items, item)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	if len(items) == 0 {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "script.none_found",
			Message:    "scripts facet is enabled but no script packages were found",
			File:       root,
			Field:      "facets.scripts",
			Suggestion: "create scripts/<script>/loom.script.yaml or disable the scripts facet",
		})
	}
	return items
}

func validateScriptPackage(loaded LoadedProject, contract ProjectContract, folderName, packageRoot string, add func(Diagnostic)) ScriptFacetItem {
	manifestPath := filepath.Join(packageRoot, "loom.script.yaml")
	folder := filepath.ToSlash(mustRelPath(loaded.RootPath, packageRoot))
	item := ScriptFacetItem{
		Key:              folderName,
		Folder:           folder,
		ManifestPath:     filepath.ToSlash(manifestPath),
		ActivationStatus: ScriptActivationStatusDisabled,
	}
	if !pathExists(manifestPath) {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "script.manifest_missing",
			Message:    "script package is missing loom.script.yaml",
			File:       manifestPath,
			Field:      "scripts." + folderName,
			Suggestion: "add loom.script.yaml or remove the script package folder",
		})
		return item
	}

	manifest, err := scripts.LoadManifest(manifestPath)
	if err != nil {
		add(Diagnostic{
			Severity: SeverityError,
			Code:     "script.manifest_invalid",
			Message:  "script manifest is invalid: " + err.Error(),
			File:     manifestPath,
			Field:    "scripts." + folderName,
		})
		return item
	}
	item.Key = manifest.ID
	item.Manifest = manifest
	item.ManifestHash, err = scripts.HashManifest(manifest)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "script.manifest_hash_failed", Message: "could not hash script manifest: " + err.Error(), File: manifestPath, Field: "scripts." + manifest.ID})
	}
	item.PackageHash, err = scripts.HashPackage(packageRoot)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "script.package_hash_failed", Message: "could not hash script package: " + err.Error(), File: manifestPath, Field: "scripts." + manifest.ID})
	}
	validateScriptEntrypoint(packageRoot, manifestPath, manifest, add)
	validateScriptUsageDocuments(packageRoot, manifestPath, manifest, add)

	exposurePath := filepath.Join(packageRoot, "loom.exposure.yaml")
	if !pathExists(exposurePath) {
		return item
	}
	exposure, payload, err := LoadScriptExposure(exposurePath)
	if err != nil {
		add(Diagnostic{
			Severity: SeverityError,
			Code:     "script_exposure.parse_failed",
			Message:  "script exposure could not be parsed: " + err.Error(),
			File:     exposurePath,
			Field:    "scripts." + manifest.ID + ".exposure",
		})
		return item
	}
	exposure = validateScriptExposure(contract, manifest.ID, exposurePath, exposure, add)
	item.ExposurePath = filepath.ToSlash(exposurePath)
	item.ExposureHash = hashBytesURI(payload)
	item.Exposure = &exposure
	item.Exposed = exposure.Expose.Enabled
	item.ProviderKey = exposure.Expose.Provider
	item.Endpoint = exposure.Expose.Endpoint
	item.Form = exposure.Capability.Form
	item.RiskLevel = exposure.Capability.RiskLevel
	item.ExecutionMode = exposure.Execution.DefaultMode
	item.WaitTimeoutSeconds = exposure.Execution.WaitTimeoutSeconds
	item.Credentials = exposure.Credentials
	item.CredentialJSON = credentialRequirementsJSON(exposure.Credentials)
	if exposure.Expose.Enabled {
		item.ProviderAddress = contract.Project.OwnerNode + "@" + item.ProviderKey
		item.CapabilityAddress = item.ProviderAddress + "." + item.Endpoint
		if normalized, err := capabilities.NormalizeProviderAddress(item.ProviderAddress); err == nil {
			item.ProviderAddress = normalized
		}
		if normalized, err := capabilities.NormalizeAddress(item.CapabilityAddress); err == nil {
			item.CapabilityAddress = normalized
		}
		item.ActivationStatus = ScriptActivationStatusPending
	}
	return item
}

func validateScriptEntrypoint(packageRoot, manifestPath string, manifest scripts.Manifest, add func(Diagnostic)) {
	if len(manifest.Entrypoint.Command) == 0 {
		return
	}
	first := strings.TrimSpace(manifest.Entrypoint.Command[0])
	if strings.HasPrefix(first, "./") {
		validateScriptEntrypointFile(packageRoot, manifestPath, first, true, add)
		return
	}
	if isScriptInterpreter(first) {
		validateInterpreterEntrypointArgs(packageRoot, manifestPath, manifest.Entrypoint.Command[1:], add)
	}
}

func validateInterpreterEntrypointArgs(packageRoot, manifestPath string, args []string, add func(Diagnostic)) {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		if interpreterFlagConsumesNext(arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if looksLikePackageEntrypointFile(arg) {
			validateScriptEntrypointFile(packageRoot, manifestPath, arg, false, add)
			return
		}
	}
}

func validateScriptEntrypointFile(packageRoot, manifestPath, commandPart string, requireExecutable bool, add func(Diagnostic)) {
	rel := strings.TrimSpace(commandPart)
	rel = strings.Trim(rel, `"'`)
	rel = strings.TrimPrefix(rel, "./")
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		add(Diagnostic{Severity: SeverityError, Code: "script.entrypoint_missing", Message: "script entrypoint does not resolve inside the script package: " + commandPart, File: manifestPath, Field: "entrypoint.command"})
		return
	}
	path := filepath.Join(packageRoot, rel)
	info, err := os.Stat(path)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "script.entrypoint_missing", Message: "script entrypoint does not exist: " + commandPart, File: manifestPath, Field: "entrypoint.command"})
		return
	}
	if requireExecutable && info.Mode().IsRegular() && info.Mode().Perm()&0o111 == 0 {
		add(Diagnostic{Severity: SeverityError, Code: "script.entrypoint_not_executable", Message: "script entrypoint is not executable: " + commandPart, File: manifestPath, Field: "entrypoint.command"})
	}
}

func isScriptInterpreter(command string) bool {
	base := filepath.Base(strings.TrimSpace(command))
	switch base {
	case "bash", "sh", "python", "python3", "node", "deno":
		return true
	default:
		return false
	}
}

func interpreterFlagConsumesNext(flag string) bool {
	switch strings.TrimSpace(flag) {
	case "-c", "-e", "--eval", "--command":
		return true
	default:
		return false
	}
}

func looksLikePackageEntrypointFile(arg string) bool {
	arg = strings.Trim(strings.TrimSpace(arg), `"'`)
	if arg == "" || filepath.IsAbs(arg) || strings.Contains(arg, "://") {
		return false
	}
	if strings.HasPrefix(arg, "./") || strings.Contains(arg, "/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(arg)) {
	case ".sh", ".bash", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".rb", ".pl":
		return true
	default:
		return false
	}
}

func validateScriptUsageDocuments(packageRoot, manifestPath string, manifest scripts.Manifest, add func(Diagnostic)) {
	for _, doc := range manifest.UsageDocuments {
		path := filepath.Join(packageRoot, filepath.FromSlash(doc.Path))
		if !pathExists(path) {
			add(Diagnostic{Severity: SeverityWarning, Code: "script.usage_document_missing", Message: "script usage document does not exist: " + doc.Path, File: manifestPath, Field: "usage_documents"})
		}
	}
}

func ignoredScriptPackageDir(name string) bool {
	switch name {
	case "node_modules", "tmp", "result", ".cache", ".git":
		return true
	default:
		return false
	}
}

func mustRelPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	if rel == "." {
		return ""
	}
	return rel
}

func scriptSummary(scripts []ScriptFacetItem) (discovered, exposed int) {
	for _, script := range scripts {
		if script.ManifestPath == "" {
			continue
		}
		discovered++
		if script.Exposed {
			exposed++
		}
	}
	return discovered, exposed
}

func scriptCapabilityAddresses(scripts []ScriptFacetItem) []string {
	out := []string{}
	for _, script := range scripts {
		if script.CapabilityAddress != "" {
			out = append(out, script.CapabilityAddress)
		}
	}
	sort.Strings(out)
	return out
}

func scriptClassNameForProject(slug string) string {
	out := strings.ReplaceAll(strings.TrimSpace(slug), "-", "_")
	if out == "" {
		return "project"
	}
	return out
}

func ScriptVersionLabel(versionLabel, packageHash string) string {
	versionLabel = strings.TrimSpace(versionLabel)
	if versionLabel == "" {
		versionLabel = "0.1.0"
	}
	hash := strings.TrimPrefix(strings.TrimSpace(packageHash), "sha256:")
	if len(hash) >= 12 {
		return versionLabel + "-" + hash[:12]
	}
	return versionLabel
}

func ProjectScriptSlug(projectSlug, scriptKey string) string {
	projectSlug = strings.TrimSpace(projectSlug)
	scriptKey = strings.TrimSpace(scriptKey)
	base := strings.ReplaceAll(projectSlug, "-", "_") + "__" + scriptKey
	if base == "__" {
		base = "project_script"
	}
	base = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, strings.ToLower(base))
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "p_" + base
	}
	if len(base) <= 63 {
		return base
	}
	sum := sha256.Sum256([]byte(projectSlug + "\x00" + scriptKey))
	suffix := "_" + hex.EncodeToString(sum[:])[:12]
	return strings.TrimRight(base[:63-len(suffix)], "_-") + suffix
}

func ProjectWorkflowSlug(projectSlug, workflowKey string) string {
	return ProjectScriptSlug(projectSlug, strings.TrimSpace(workflowKey))
}

func ProjectConnectorScriptSlug(projectSlug, connectorKey, endpoint string) string {
	connectorKey = strings.ReplaceAll(strings.TrimSpace(connectorKey), ".", "_")
	endpoint = strings.ReplaceAll(strings.TrimSpace(endpoint), ".", "_")
	return ProjectScriptSlug(projectSlug, strings.Trim(connectorKey+"__"+endpoint, "_"))
}

func sideEffectsObject(effects []string) map[string]any {
	cleaned := []string{}
	for _, effect := range effects {
		effect = strings.TrimSpace(effect)
		if effect != "" {
			cleaned = append(cleaned, effect)
		}
	}
	return map[string]any{"effects": cleaned}
}

func approvalRequirementsObject(required bool) map[string]any {
	return map[string]any{"required": required}
}

func policyRequirementsObject() map[string]any {
	return map[string]any{}
}

func jsonObjectFromMap(value map[string]any) []byte {
	if value == nil {
		value = map[string]any{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}
