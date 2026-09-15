package filepolicy

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

type Resolver struct {
	root        string
	policyRoot  string
	pathPrefix  string
	profile     Profile
	policyFiles []PolicyFile
	options     ResolverOptions
	fingerprint string
}

var compiledResolverCache sync.Map

func NewResolver(root string, profile Profile, options ResolverOptions) (*Resolver, error) {
	if !validProfiles[profile] {
		return nil, fmt.Errorf("unsupported file-policy profile %q", profile)
	}
	canonicalRoot, err := canonicalPolicyRoot(root)
	if err != nil {
		return nil, err
	}
	if profile == ProfileExact {
		options.DiscoverUserRules = false
	}
	policyRoot := canonicalRoot
	if strings.TrimSpace(options.PolicyRoot) != "" {
		policyRoot, err = canonicalPolicyRoot(options.PolicyRoot)
		if err != nil {
			return nil, err
		}
	}
	pathPrefix, err := filepath.Rel(policyRoot, canonicalRoot)
	if err != nil || pathPrefix == ".." || strings.HasPrefix(pathPrefix, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("file-policy root %q is outside policy boundary %q", canonicalRoot, policyRoot)
	}
	pathPrefix = filepath.ToSlash(pathPrefix)
	if pathPrefix == "." {
		pathPrefix = ""
	}
	files := []PolicyFile{}
	if options.DiscoverUserRules {
		files, err = discoverPolicyFiles(policyRoot, canonicalRoot, options)
		if err != nil {
			return nil, err
		}
	}
	fingerprint := policyFingerprint(profile, files, options)
	cacheKey := canonicalRoot + "\x00" + policyRoot + "\x00" + fingerprint
	if cached, ok := compiledResolverCache.Load(cacheKey); ok {
		return cached.(*Resolver), nil
	}
	resolver := &Resolver{
		root:        canonicalRoot,
		policyRoot:  policyRoot,
		pathPrefix:  pathPrefix,
		profile:     profile,
		policyFiles: files,
		options: ResolverOptions{
			DiscoverUserRules: options.DiscoverUserRules,
			ContractIncludes:  append([]string{}, options.ContractIncludes...),
			ContractExcludes:  append([]string{}, options.ContractExcludes...),
		},
		fingerprint: fingerprint,
	}
	compiledResolverCache.Store(cacheKey, resolver)
	return resolver, nil
}

func canonicalPolicyRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("file-policy root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve file-policy root %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve file-policy root %q: %w", root, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect file-policy root %q: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("file-policy root %q is not a directory", root)
	}
	return filepath.Clean(resolved), nil
}

func discoverPolicyFiles(policyRoot, root string, options ResolverOptions) ([]PolicyFile, error) {
	files := []PolicyFile{}
	seen := map[string]bool{}
	var policyBytes int64
	checkBudget := func(info fs.FileInfo) error {
		if !options.DiscoveryDeadline.IsZero() && time.Now().After(options.DiscoveryDeadline) {
			return fmt.Errorf("file-policy discovery duration budget exhausted")
		}
		if info != nil {
			policyBytes += info.Size()
			if options.MaxPolicyBytes > 0 && policyBytes > options.MaxPolicyBytes {
				return fmt.Errorf("file-policy discovery byte budget exhausted")
			}
		}
		return nil
	}
	for directory := root; ; directory = filepath.Dir(directory) {
		policyPath := filepath.Join(directory, ".loomignore")
		if info, err := os.Lstat(policyPath); err == nil {
			if err := checkBudget(info); err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf(".loomignore %q must not be a symlink", policyPath)
			}
			content, err := os.ReadFile(policyPath)
			if err != nil {
				return nil, fmt.Errorf("read .loomignore %q: %w", policyPath, err)
			}
			parsed, err := parsePolicyFile(policyRoot, policyPath, content)
			if err != nil {
				return nil, err
			}
			files = append(files, parsed)
			seen[filepath.Clean(policyPath)] = true
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect .loomignore %q: %w", policyPath, err)
		}
		if directory == policyRoot {
			break
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil, fmt.Errorf("policy boundary %q is not an ancestor of %q", policyRoot, root)
		}
	}
	discoveredEntries := 0
	err := filepath.WalkDir(root, func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("discover .loomignore under %q: %w", root, walkErr)
		}
		if pathValue == root {
			return nil
		}
		discoveredEntries++
		if options.MaxDiscoveryEntries > 0 && discoveredEntries > options.MaxDiscoveryEntries {
			return fmt.Errorf("file-policy discovery entry budget exhausted")
		}
		if err := checkBudget(nil); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.Name() == ".loomignore" {
				return fmt.Errorf(".loomignore %q must not be a symlink", pathValue)
			}
			return nil
		}
		if entry.IsDir() || entry.Name() != ".loomignore" {
			return nil
		}
		if seen[filepath.Clean(pathValue)] {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := checkBudget(info); err != nil {
			return err
		}
		content, err := os.ReadFile(pathValue)
		if err != nil {
			return fmt.Errorf("read .loomignore %q: %w", pathValue, err)
		}
		parsed, err := parsePolicyFile(policyRoot, pathValue, content)
		if err != nil {
			return err
		}
		files = append(files, parsed)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(left, right int) bool {
		leftDepth := strings.Count(files[left].BaseDir, "/")
		rightDepth := strings.Count(files[right].BaseDir, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return files[left].RelativePath < files[right].RelativePath
	})
	return files, nil
}

func policyFingerprint(profile Profile, files []PolicyFile, options ResolverOptions) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%t\x00", BuiltInPolicyVersion, profile, options.DiscoverUserRules)
	for _, file := range files {
		fmt.Fprintf(hash, "%s\x00%s\x00", file.RelativePath, file.ContentHash)
	}
	for _, pattern := range options.ContractIncludes {
		fmt.Fprintf(hash, "include:%s\x00", pattern)
	}
	for _, pattern := range options.ContractExcludes {
		fmt.Fprintf(hash, "exclude:%s\x00", pattern)
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func (resolver *Resolver) Root() string { return resolver.root }

func (resolver *Resolver) PolicyRoot() string { return resolver.policyRoot }

func (resolver *Resolver) Profile() Profile { return resolver.profile }

func (resolver *Resolver) Fingerprint() string { return resolver.fingerprint }

func (resolver *Resolver) PolicyFiles() []PolicyFile {
	result := make([]PolicyFile, len(resolver.policyFiles))
	copy(result, resolver.policyFiles)
	return result
}

// MayIncludeDescendant reports whether traversal must enter an ignored
// directory to honor a closer user negation. Mandatory safety paths never use
// this escape hatch.
func (resolver *Resolver) MayIncludeDescendant(relativeDirectory string) bool {
	decision := EvaluateBuiltIn(resolver.profile, relativeDirectory)
	if decision.RuleCategory == RuleCategoryMandatorySafety {
		return false
	}
	for _, file := range resolver.policyFiles {
		for _, rule := range file.Rules {
			if rule.Negated {
				return true
			}
		}
	}
	return false
}

func (resolver *Resolver) Resolve(relativePath string, isDir bool) (Resolution, error) {
	normalized, err := validatedRelativePolicyPath(relativePath)
	if err != nil {
		return Resolution{}, err
	}
	decision := EvaluateBuiltIn(resolver.profile, normalized)
	trace := []Decision{decision}
	if decision.RuleCategory == RuleCategoryMandatorySafety {
		return Resolution{Decision: decision, Trace: trace}, nil
	}
	if normalized == ".loomignore" || strings.HasSuffix(normalized, "/.loomignore") {
		decision.Included = true
		decision.RuleCategory = RuleCategoryNone
		decision.Pattern = ""
		trace = append(trace, decision)
		return Resolution{Decision: decision, Trace: trace}, nil
	}
	if resolver.options.DiscoverUserRules {
		policyPath := normalized
		if resolver.pathPrefix != "" {
			policyPath = resolver.pathPrefix
			if normalized != "" {
				policyPath += "/" + normalized
			}
		}
		for _, file := range resolver.policyFiles {
			for _, rule := range file.Rules {
				matched, err := ruleMatches(rule, policyPath, isDir)
				if err != nil {
					return Resolution{}, err
				}
				if !matched {
					continue
				}
				decision.Included = rule.Negated
				decision.RuleCategory = RuleCategoryUser
				decision.Pattern = rule.OriginalPattern
				decision.SourceFile = rule.SourceFile
				decision.SourceLine = rule.SourceLine
				decision.Negated = rule.Negated
				trace = append(trace, decision)
			}
		}
	}
	for _, pattern := range resolver.options.ContractIncludes {
		matched, err := contractPatternMatches(pattern, normalized, isDir)
		if err != nil {
			return Resolution{}, err
		}
		if matched {
			decision.Included = true
			decision.RuleCategory = RuleCategoryContract
			decision.Pattern = pattern
			decision.SourceFile = ""
			decision.SourceLine = 0
			decision.Negated = true
			trace = append(trace, decision)
		}
	}
	for _, pattern := range resolver.options.ContractExcludes {
		matched, err := contractPatternMatches(pattern, normalized, isDir)
		if err != nil {
			return Resolution{}, err
		}
		if matched {
			decision.Included = false
			decision.RuleCategory = RuleCategoryContract
			decision.Pattern = pattern
			decision.SourceFile = ""
			decision.SourceLine = 0
			decision.Negated = false
			trace = append(trace, decision)
		}
	}
	return Resolution{Decision: decision, Trace: trace}, nil
}

func (resolver *Resolver) ResolvePath(pathValue string) (Resolution, error) {
	absolute := pathValue
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(resolver.root, absolute)
	}
	absolute = filepath.Clean(absolute)
	relative, err := filepath.Rel(resolver.root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Resolution{}, fmt.Errorf("path %q escapes file-policy root %q", pathValue, resolver.root)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return Resolution{}, fmt.Errorf("inspect policy path %q: %w", pathValue, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve policy symlink %q: %w", pathValue, err)
		}
		resolvedRelative, err := filepath.Rel(resolver.root, resolved)
		if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
			return Resolution{}, fmt.Errorf("symlink %q escapes file-policy root %q", pathValue, resolver.root)
		}
	}
	return resolver.Resolve(filepath.ToSlash(relative), info.IsDir())
}

func validatedRelativePolicyPath(value string) (string, error) {
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("file-policy path must be relative: %q", value)
	}
	cleaned := filepath.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file-policy path escapes root: %q", value)
	}
	normalized := normalizePolicyPath(value)
	return normalized, nil
}

func ruleMatches(rule Rule, relativePath string, isDir bool) (bool, error) {
	candidate := relativePath
	if rule.BaseDir != "" {
		if candidate == rule.BaseDir {
			return false, nil
		}
		prefix := rule.BaseDir + "/"
		if !strings.HasPrefix(candidate, prefix) {
			return false, nil
		}
		candidate = strings.TrimPrefix(candidate, prefix)
	}
	parts := strings.Split(candidate, "/")
	if !rule.Anchored && !strings.Contains(rule.NormalizedPattern, "/") {
		for index, part := range parts {
			matched, err := doublestar.Match(rule.NormalizedPattern, part)
			if err != nil {
				return false, err
			}
			if matched && (!rule.DirectoryOnly || index < len(parts)-1 || isDir) {
				return true, nil
			}
		}
		return false, nil
	}
	for length := 1; length <= len(parts); length++ {
		prefix := strings.Join(parts[:length], "/")
		matched, err := doublestar.Match(rule.NormalizedPattern, prefix)
		if err != nil {
			return false, err
		}
		prefixIsDir := length < len(parts) || isDir
		if matched && (!rule.DirectoryOnly || prefixIsDir) {
			return true, nil
		}
	}
	return false, nil
}

func contractPatternMatches(pattern, relativePath string, isDir bool) (bool, error) {
	pattern = strings.TrimSpace(strings.TrimPrefix(filepath.ToSlash(pattern), "./"))
	directoryOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return false, fmt.Errorf("empty contract ignore pattern")
	}
	rule := Rule{NormalizedPattern: pattern, DirectoryOnly: directoryOnly}
	return ruleMatches(rule, relativePath, isDir)
}
