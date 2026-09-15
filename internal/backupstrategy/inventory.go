package backupstrategy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type Service struct {
	Remote RemoteInventorySource
}

// Inventory reads filesystem metadata and an optional declared remote summary.
// It never opens file payloads, follows a discovered symlink, creates a path,
// or mutates local or remote state.
func (service Service) Inventory(ctx context.Context, config Config) (Inventory, error) {
	if err := ctx.Err(); err != nil {
		return Inventory{}, err
	}
	rootPath, specs, err := validateConfig(config)
	if err != nil {
		return Inventory{}, err
	}
	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		return Inventory{}, fmt.Errorf("%w: inspect local root: %v", ErrInvalidConfig, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return Inventory{}, fmt.Errorf("%w: local root must be a real directory", ErrInvalidConfig)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return Inventory{}, fmt.Errorf("open confined local root: %w", err)
	}
	defer root.Close()
	openedInfo, err := root.Lstat(".")
	if err != nil || !os.SameFile(rootInfo, openedInfo) {
		return Inventory{}, fmt.Errorf("%w: local root changed during preflight", ErrInvalidConfig)
	}

	report := Inventory{
		SchemaVersion:   inventorySchemaVersion,
		LocalRoot:       rootPath,
		Components:      []ComponentInventory{},
		CleanupBlockers: []string{},
	}
	rootEntry, metadataKnown, err := inventoryEntry(".", openedInfo)
	if err != nil {
		return Inventory{}, err
	}
	if !metadataKnown {
		return Inventory{}, fmt.Errorf("inventory local root: device, inode, link-count, or allocated-block metadata is unavailable")
	}
	report.LocalRootEntry = rootEntry
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return Inventory{}, fmt.Errorf("read local inventory root: %w", err)
	}
	observed := make(map[string]bool, len(entries))
	for _, directoryEntry := range entries {
		if err := ctx.Err(); err != nil {
			return Inventory{}, err
		}
		name := directoryEntry.Name()
		observed[name] = true
		spec, declared := specs[name]
		if !declared {
			spec = ComponentSpec{RelativePath: name, Class: ComponentUnknown}
		}
		component, err := scanComponent(ctx, root, spec)
		if err != nil {
			return Inventory{}, err
		}
		report.Components = append(report.Components, component)
	}
	for name, spec := range specs {
		if observed[name] {
			continue
		}
		report.Components = append(report.Components, ComponentInventory{
			RelativePath:   name,
			Class:          spec.Class,
			Protected:      spec.Protected || spec.Class.protectedByDefault(),
			Safe:           false,
			RestorePoint:   false,
			ReclaimPosture: ReclaimBlocked,
			Entries:        []EntryInventory{},
			Findings: []Finding{{
				Code:         FindingMissingComponent,
				RelativePath: name,
				Detail:       "declared component is absent",
			}},
		})
	}
	sort.Slice(report.Components, func(left, right int) bool {
		return report.Components[left].RelativePath < report.Components[right].RelativePath
	})
	if err := accountLocalInventory(&report); err != nil {
		return Inventory{}, err
	}
	if service.Remote != nil {
		remote, err := service.Remote.Inventory(ctx)
		if err != nil {
			return Inventory{}, fmt.Errorf("read declared remote inventory: %w", err)
		}
		remote.Archives = append([]RemoteArchiveSummary(nil), remote.Archives...)
		if err := normalizeRemoteInventory(&remote); err != nil {
			return Inventory{}, err
		}
		report.Remote = &remote
	}
	digest, err := inventoryDigest(report)
	if err != nil {
		return Inventory{}, err
	}
	report.Digest = digest
	return report, nil
}

// RequireDigest rescans read-only state and rejects a stale accounting
// checkpoint. It intentionally performs no plan or apply action.
func (service Service) RequireDigest(ctx context.Context, config Config, expected string) (Inventory, error) {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return Inventory{}, fmt.Errorf("%w: expected digest is required", ErrInvalidConfig)
	}
	report, err := service.Inventory(ctx, config)
	if err != nil {
		return Inventory{}, err
	}
	if report.Digest != expected {
		return report, &InventoryChangedError{Expected: expected, Actual: report.Digest}
	}
	return report, nil
}

func validateConfig(config Config) (string, map[string]ComponentSpec, error) {
	rootPath := filepath.Clean(strings.TrimSpace(config.LocalRoot))
	if rootPath == "." || rootPath == "" || !filepath.IsAbs(rootPath) {
		return "", nil, fmt.Errorf("%w: local root must be absolute", ErrInvalidConfig)
	}
	specs := make(map[string]ComponentSpec, len(config.Components))
	for _, input := range config.Components {
		name := filepath.ToSlash(strings.TrimSpace(input.RelativePath))
		if !fs.ValidPath(name) || name == "." || strings.Contains(name, "/") {
			return "", nil, fmt.Errorf("%w: component %q must be one confined top-level name", ErrInvalidConfig, input.RelativePath)
		}
		if !input.Class.valid() {
			return "", nil, fmt.Errorf("%w: component %q has unsupported class %q", ErrInvalidConfig, name, input.Class)
		}
		if _, exists := specs[name]; exists {
			return "", nil, fmt.Errorf("%w: component %q is declared more than once", ErrInvalidConfig, name)
		}
		input.RelativePath = name
		specs[name] = input
	}
	return rootPath, specs, nil
}

func scanComponent(ctx context.Context, root *os.Root, spec ComponentSpec) (ComponentInventory, error) {
	component := ComponentInventory{
		RelativePath: spec.RelativePath,
		Class:        spec.Class,
		Protected:    spec.Protected || spec.Class.protectedByDefault(),
		Safe:         true,
		Entries:      []EntryInventory{},
		Findings:     []Finding{},
	}
	if err := scanNode(ctx, root, spec.RelativePath, spec.RelativePath, &component); err != nil {
		return ComponentInventory{}, err
	}
	sort.Slice(component.Entries, func(left, right int) bool {
		return component.Entries[left].RelativePath < component.Entries[right].RelativePath
	})
	sort.Slice(component.Findings, func(left, right int) bool {
		if component.Findings[left].RelativePath == component.Findings[right].RelativePath {
			return component.Findings[left].Code < component.Findings[right].Code
		}
		return component.Findings[left].RelativePath < component.Findings[right].RelativePath
	})
	component.RestorePoint = component.Safe && (component.Class == ComponentSuccessfulGeneration || component.Class == ComponentProtectedMilestone)
	return component, nil
}

func scanNode(ctx context.Context, parent *os.Root, name, relative string, component *ComponentInventory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := parent.Lstat(name)
	if err != nil {
		component.Safe = false
		component.Findings = append(component.Findings, Finding{Code: FindingUnreadable, RelativePath: relative, Detail: "entry could not be inspected without following links"})
		return nil
	}
	entry, metadataKnown, err := inventoryEntry(relative, info)
	if err != nil {
		return err
	}
	component.Entries = append(component.Entries, entry)
	if err := addUint64(&component.Accounting.LogicalBytes, entry.LogicalBytes); err != nil {
		return fmt.Errorf("account logical bytes for %s: %w", relative, err)
	}
	if !metadataKnown {
		component.Safe = false
		component.Findings = append(component.Findings, Finding{Code: FindingMetadataUnavailable, RelativePath: relative, Detail: "device, inode, link-count, or allocated-block metadata is unavailable"})
	}
	switch entry.Kind {
	case EntrySymlink:
		component.Safe = false
		component.Findings = append(component.Findings, Finding{Code: FindingSymlink, RelativePath: relative, Detail: "symlink was recorded with lstat and not followed"})
		return nil
	case EntrySpecial:
		component.Safe = false
		component.Findings = append(component.Findings, Finding{Code: FindingSpecialFile, RelativePath: relative, Detail: "special filesystem entry is not eligible for backup reclamation"})
		return nil
	case EntryRegularFile:
		return nil
	case EntryDirectory:
		childRoot, openErr := parent.OpenRoot(name)
		if openErr != nil {
			component.Safe = false
			component.Findings = append(component.Findings, Finding{Code: FindingUnreadable, RelativePath: relative, Detail: "directory could not be opened within the confined root"})
			return nil
		}
		defer childRoot.Close()
		openedInfo, statErr := childRoot.Lstat(".")
		if statErr != nil || !os.SameFile(info, openedInfo) {
			component.Safe = false
			component.Findings = append(component.Findings, Finding{Code: FindingUnreadable, RelativePath: relative, Detail: "directory identity changed during no-follow traversal"})
			return nil
		}
		children, readErr := fs.ReadDir(childRoot.FS(), ".")
		if readErr != nil {
			component.Safe = false
			component.Findings = append(component.Findings, Finding{Code: FindingUnreadable, RelativePath: relative, Detail: "directory entries could not be read"})
			return nil
		}
		for _, child := range children {
			childRelative := path.Join(relative, child.Name())
			if err := scanNode(ctx, childRoot, child.Name(), childRelative, component); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func inventoryEntry(relative string, info os.FileInfo) (EntryInventory, bool, error) {
	logical := uint64(0)
	if info.Size() > 0 {
		logical = uint64(info.Size())
	}
	device, deviceOK := statUint(info.Sys(), "Dev")
	inode, inodeOK := statUint(info.Sys(), "Ino")
	links, linksOK := statUint(info.Sys(), "Nlink")
	blocks, blocksOK := statUint(info.Sys(), "Blocks")
	if blocks > math.MaxUint64/512 {
		return EntryInventory{}, false, fmt.Errorf("allocated-byte overflow for %s", relative)
	}
	entry := EntryInventory{
		RelativePath:     filepath.ToSlash(relative),
		Kind:             kindForMode(info.Mode()),
		Mode:             uint32(info.Mode()),
		LogicalBytes:     logical,
		AllocatedBytes:   blocks * 512,
		DeviceID:         device,
		Inode:            inode,
		LinkCount:        links,
		ModifiedUnixNano: info.ModTime().UnixNano(),
		ChangedUnixNano:  statChangeUnixNano(info.Sys()),
		IdentityKnown:    deviceOK && inodeOK && linksOK && blocksOK,
	}
	return entry, entry.IdentityKnown, nil
}

func kindForMode(mode os.FileMode) EntryKind {
	switch {
	case mode&os.ModeSymlink != 0:
		return EntrySymlink
	case mode.IsDir():
		return EntryDirectory
	case mode.IsRegular():
		return EntryRegularFile
	default:
		return EntrySpecial
	}
}

func statUint(sys any, name string) (uint64, bool) {
	value := reflect.ValueOf(sys)
	if !value.IsValid() {
		return 0, false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, false
	}
	field := value.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Int() < 0 {
			return 0, false
		}
		return uint64(field.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint(), true
	default:
		return 0, false
	}
}

func statChangeUnixNano(sys any) int64 {
	value := reflect.ValueOf(sys)
	if !value.IsValid() {
		return 0
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0
	}
	for _, name := range []string{"Ctim", "Ctimespec"} {
		field := value.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.Struct {
			continue
		}
		seconds, secondsOK := signedStatField(field.FieldByName("Sec"))
		nanoseconds, nanosecondsOK := signedStatField(field.FieldByName("Nsec"))
		if secondsOK && nanosecondsOK && seconds <= math.MaxInt64/1_000_000_000 {
			return seconds*1_000_000_000 + nanoseconds
		}
	}
	return 0
}

func signedStatField(field reflect.Value) (int64, bool) {
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return field.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if field.Uint() > math.MaxInt64 {
			return 0, false
		}
		return int64(field.Uint()), true
	default:
		return 0, false
	}
}

type inodeKey struct {
	device uint64
	inode  uint64
}

type inodeReference struct {
	component int
	entry     int
}

type inodeAllocation struct {
	allocated uint64
	kind      EntryKind
	linkCount uint64
	refs      []inodeReference
}

func accountLocalInventory(report *Inventory) error {
	if err := addUint64(&report.LocalTotals.LogicalBytes, report.LocalRootEntry.LogicalBytes); err != nil {
		return fmt.Errorf("account local root logical bytes: %w", err)
	}
	if err := addUint64(&report.LocalTotals.AllocatedBytes, report.LocalRootEntry.AllocatedBytes); err != nil {
		return fmt.Errorf("account local root allocated bytes: %w", err)
	}
	if err := addUint64(&report.LocalTotals.UniqueBytes, report.LocalRootEntry.AllocatedBytes); err != nil {
		return fmt.Errorf("account local root unique bytes: %w", err)
	}
	if err := addUint64(&report.LocalTotals.ProtectedBytes, report.LocalRootEntry.AllocatedBytes); err != nil {
		return fmt.Errorf("account local root protected bytes: %w", err)
	}
	inodes := make(map[inodeKey]*inodeAllocation)
	for componentIndex := range report.Components {
		component := &report.Components[componentIndex]
		if err := addUint64(&report.LocalTotals.LogicalBytes, component.Accounting.LogicalBytes); err != nil {
			return fmt.Errorf("account local logical bytes: %w", err)
		}
		for entryIndex, entry := range component.Entries {
			if !entry.IdentityKnown {
				continue
			}
			key := inodeKey{device: entry.DeviceID, inode: entry.Inode}
			allocation, exists := inodes[key]
			if !exists {
				allocation = &inodeAllocation{allocated: entry.AllocatedBytes, kind: entry.Kind, linkCount: entry.LinkCount}
				inodes[key] = allocation
			} else if allocation.allocated != entry.AllocatedBytes || allocation.kind != entry.Kind || allocation.linkCount != entry.LinkCount {
				return fmt.Errorf("inconsistent inode identity %d:%d while inventorying %s", entry.DeviceID, entry.Inode, entry.RelativePath)
			}
			allocation.refs = append(allocation.refs, inodeReference{component: componentIndex, entry: entryIndex})
		}
	}

	for _, allocation := range inodes {
		if err := addUint64(&report.LocalTotals.AllocatedBytes, allocation.allocated); err != nil {
			return err
		}
		shared := len(allocation.refs) > 1 || allocation.kind == EntryRegularFile && allocation.linkCount > 1
		if shared {
			if err := addUint64(&report.LocalTotals.SharedBytes, allocation.allocated); err != nil {
				return err
			}
		} else if err := addUint64(&report.LocalTotals.UniqueBytes, allocation.allocated); err != nil {
			return err
		}

		componentRefs := make(map[int]int)
		for _, reference := range allocation.refs {
			componentRefs[reference.component]++
		}
		for componentIndex := range componentRefs {
			component := &report.Components[componentIndex]
			if err := addUint64(&component.Accounting.AllocatedBytes, allocation.allocated); err != nil {
				return err
			}
			if shared {
				if err := addUint64(&component.Accounting.SharedBytes, allocation.allocated); err != nil {
					return err
				}
			} else if err := addUint64(&component.Accounting.UniqueBytes, allocation.allocated); err != nil {
				return err
			}
			switch component.Class {
			case ComponentIncompleteStaging:
				if err := addUint64(&component.Accounting.StagingBytes, allocation.allocated); err != nil {
					return err
				}
			case ComponentUnknown:
				if err := addUint64(&component.Accounting.UnknownBytes, allocation.allocated); err != nil {
					return err
				}
			}
			if component.Protected {
				if err := addUint64(&component.Accounting.ProtectedBytes, allocation.allocated); err != nil {
					return err
				}
			}
		}

		if allocationHasClass(report, allocation, ComponentIncompleteStaging) {
			if err := addUint64(&report.LocalTotals.StagingBytes, allocation.allocated); err != nil {
				return err
			}
		}
		if allocationHasClass(report, allocation, ComponentUnknown) {
			if err := addUint64(&report.LocalTotals.UnknownBytes, allocation.allocated); err != nil {
				return err
			}
		}
		if allocationIsProtected(report, allocation) {
			if err := addUint64(&report.LocalTotals.ProtectedBytes, allocation.allocated); err != nil {
				return err
			}
		}

		if len(componentRefs) == 1 {
			for componentIndex, observedLinks := range componentRefs {
				component := &report.Components[componentIndex]
				allLinksObserved := allocation.kind != EntryRegularFile || allocation.linkCount == uint64(observedLinks)
				if component.Safe && component.Class == ComponentSuccessfulGeneration && !component.Protected && allLinksObserved {
					if err := addUint64(&component.Accounting.PotentialReclaimableBytes, allocation.allocated); err != nil {
						return err
					}
					if err := addUint64(&report.LocalTotals.PotentialReclaimableBytes, allocation.allocated); err != nil {
						return err
					}
				}
			}
		}
	}

	blockers := make(map[string]struct{})
	for index := range report.Components {
		component := &report.Components[index]
		switch {
		case !component.Safe:
			component.ReclaimPosture = ReclaimBlocked
			blockers[BlockerUnsafeEntry] = struct{}{}
		case component.Class == ComponentUnknown:
			component.ReclaimPosture = ReclaimBlocked
			blockers[BlockerUnknownEntry] = struct{}{}
		case component.Class == ComponentIncompleteStaging:
			component.ReclaimPosture = ReclaimBlocked
			blockers[BlockerActiveStaging] = struct{}{}
		case component.Protected:
			component.ReclaimPosture = ReclaimProtected
		case component.Accounting.PotentialReclaimableBytes > 0:
			component.ReclaimPosture = ReclaimCandidate
		default:
			component.ReclaimPosture = ReclaimRetained
		}
		component.RestorePoint = component.Safe && (component.Class == ComponentSuccessfulGeneration || component.Class == ComponentProtectedMilestone)
	}
	for blocker := range blockers {
		report.CleanupBlockers = append(report.CleanupBlockers, blocker)
	}
	sort.Strings(report.CleanupBlockers)
	report.CleanupEligible = len(report.CleanupBlockers) == 0 && report.LocalTotals.PotentialReclaimableBytes > 0
	if report.CleanupEligible {
		report.LocalTotals.ReclaimableBytes = report.LocalTotals.PotentialReclaimableBytes
		for index := range report.Components {
			component := &report.Components[index]
			if component.ReclaimPosture == ReclaimCandidate {
				component.Accounting.ReclaimableBytes = component.Accounting.PotentialReclaimableBytes
			}
		}
	}
	return nil
}

func allocationHasClass(report *Inventory, allocation *inodeAllocation, class ComponentClass) bool {
	for _, reference := range allocation.refs {
		if report.Components[reference.component].Class == class {
			return true
		}
	}
	return false
}

func allocationIsProtected(report *Inventory, allocation *inodeAllocation) bool {
	for _, reference := range allocation.refs {
		if report.Components[reference.component].Protected {
			return true
		}
	}
	return false
}

func normalizeRemoteInventory(remote *RemoteInventory) error {
	remote.RepositoryID = strings.TrimSpace(remote.RepositoryID)
	if remote.RepositoryID == "" {
		return fmt.Errorf("%w: repository identity is required", ErrInvalidRemoteInventory)
	}
	partition, err := sumUint64(remote.Accounting.UniqueBytes, remote.Accounting.SharedBytes)
	if err != nil || partition != remote.Accounting.AllocatedBytes {
		return fmt.Errorf("%w: unique and shared bytes must partition repository allocation", ErrInvalidRemoteInventory)
	}
	seen := make(map[string]struct{}, len(remote.Archives))
	for index := range remote.Archives {
		archive := &remote.Archives[index]
		archive.Reference = strings.TrimSpace(archive.Reference)
		archive.Class = strings.TrimSpace(archive.Class)
		if archive.Reference == "" || archive.Class == "" {
			return fmt.Errorf("%w: archive reference and class are required", ErrInvalidRemoteInventory)
		}
		if _, exists := seen[archive.Reference]; exists {
			return fmt.Errorf("%w: duplicate archive reference %q", ErrInvalidRemoteInventory, archive.Reference)
		}
		seen[archive.Reference] = struct{}{}
	}
	sort.Slice(remote.Archives, func(left, right int) bool {
		if remote.Archives[left].Reference == remote.Archives[right].Reference {
			return remote.Archives[left].Class < remote.Archives[right].Class
		}
		return remote.Archives[left].Reference < remote.Archives[right].Reference
	})
	if remote.Archives == nil {
		remote.Archives = []RemoteArchiveSummary{}
	}
	return nil
}

func inventoryDigest(report Inventory) (string, error) {
	report.Digest = ""
	raw, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("encode inventory digest input: %w", err)
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func addUint64(target *uint64, value uint64) error {
	if math.MaxUint64-*target < value {
		return fmt.Errorf("byte count overflow")
	}
	*target += value
	return nil
}

func sumUint64(left, right uint64) (uint64, error) {
	if math.MaxUint64-left < right {
		return 0, fmt.Errorf("byte count overflow")
	}
	return left + right, nil
}
