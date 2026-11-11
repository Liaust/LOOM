// Package workspacenotes owns lightweight Markdown navigation metadata on Main.
// It neither enrolls sources nor starts knowledge extraction or embedding work.
package workspacenotes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

type Reference struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}
type Metadata struct {
	Aliases    []string       `json:"aliases"`
	Tags       []string       `json:"tags"`
	Properties []string       `json:"properties"`
	Values     map[string]any `json:"values"`
	Links      []Reference    `json:"links"`
}

var wiki = regexp.MustCompile(`!?\[\[([^\]\r\n]+)\]\]`)
var hashtag = regexp.MustCompile(`(^|[\s\[(])#([\pL\pN_][\pL\pN_/-]*)`)

func stringsFrom(value any) []string {
	result := []string{}
	switch v := value.(type) {
	case string:
		result = append(result, v)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
	}
	return result
}
func unique(values []string) []string {
	set := map[string]bool{}
	result := []string{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !set[v] {
			set[v] = true
			result = append(result, v)
		}
	}
	return result
}
func scanWiki(value string, links *[]Reference) {
	for _, m := range wiki.FindAllStringSubmatch(value, -1) {
		*links = append(*links, Reference{"wikilink", strings.TrimSpace(strings.SplitN(m[1], "|", 2)[0])})
	}
}

// Unsupported or oversized values stay unknown (absent), not coerced to strings
// or null. Property names remain available to existing completion consumers.
func propertyMetadata(data map[string]any) Metadata {
	result := Metadata{Aliases: unique(append(stringsFrom(data["aliases"]), stringsFrom(data["alias"])...)), Tags: unique(stringsFrom(data["tags"])), Properties: []string{}, Values: map[string]any{}, Links: []Reference{}}
	for key := range data {
		result.Properties = append(result.Properties, key)
	}
	sort.Strings(result.Properties)
	remaining := 64 << 10
	for _, key := range result.Properties {
		value := data[key]
		for _, text := range stringsFrom(value) {
			scanWiki(text, &result.Links)
		}
		budget := 4096
		safe, ok := safePropertyValue(value, 0, &budget)
		if !ok || !safePropertyString(key) {
			continue
		}
		raw, err := json.Marshal(map[string]any{key: safe})
		if err != nil || len(raw) > remaining {
			continue
		}
		remaining -= len(raw)
		result.Values[key] = safe
	}
	for i, tag := range result.Tags {
		result.Tags[i] = strings.TrimPrefix(tag, "#")
	}
	result.Tags = unique(result.Tags)
	return result
}

func safePropertyString(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func safePropertyValue(value any, depth int, budget *int) (any, bool) {
	*budget -= 1
	if depth > 16 || *budget < 0 {
		return nil, false
	}
	switch v := value.(type) {
	case nil, bool, int, int64, uint, uint64:
		return v, true
	case string:
		return v, len(v) <= 64<<10 && safePropertyString(v)
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case json.Number:
		return v, json.Valid([]byte(v.String()))
	case time.Time:
		return v.Format(time.RFC3339Nano), true
	case []any:
		if len(v) > *budget {
			return nil, false
		}
		out := make([]any, len(v))
		for i, item := range v {
			var ok bool
			out[i], ok = safePropertyValue(item, depth+1, budget)
			if !ok {
				return nil, false
			}
		}
		return out, true
	case map[string]any:
		if len(v) > *budget {
			return nil, false
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			if !safePropertyString(key) {
				return nil, false
			}
			safe, ok := safePropertyValue(item, depth+1, budget)
			if !ok {
				return nil, false
			}
			out[key] = safe
		}
		return out, true
	default:
		return nil, false
	}
}

func Parse(content []byte) Metadata {
	result := propertyMetadata(nil)
	source := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	source = bytes.TrimPrefix(source, []byte("\xef\xbb\xbf"))
	if bytes.HasPrefix(source, []byte("---\n")) {
		offset := 4
		for offset < len(source) {
			end := bytes.IndexByte(source[offset:], '\n')
			if end < 0 {
				end = len(source) - offset
			}
			line := strings.TrimSpace(string(source[offset : offset+end]))
			if line == "---" || line == "..." {
				var data map[string]any
				if yaml.Unmarshal(source[4:offset], &data) == nil {
					result = propertyMetadata(data)
					source = source[min(len(source), offset+end+1):]
				}
				break
			}
			offset += end + 1
		}
	}
	document := goldmark.DefaultParser().Parse(text.NewReader(source))
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.CodeSpan, *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock, *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			result.Links = append(result.Links, Reference{"markdown", string(n.Destination)})
		}
		// Text may be split into adjacent AST nodes at brackets. Scan complete inline
		// blocks, masking literal/code spans rather than hand-parsing Markdown.
		if node.Type() == ast.TypeBlock && node.HasChildren() {
			lines := node.Lines()
			if lines != nil && lines.Len() > 0 {
				start, end := lines.At(0).Start, lines.At(lines.Len()-1).Stop
				block := append([]byte(nil), source[start:end]...)
				_ = ast.Walk(node, func(child ast.Node, in bool) (ast.WalkStatus, error) {
					if !in {
						return ast.WalkContinue, nil
					}
					if _, ok := child.(*ast.CodeSpan); ok {
						for part := child.FirstChild(); part != nil; part = part.NextSibling() {
							if t, ok := part.(*ast.Text); ok {
								for i := max(start, t.Segment.Start-1); i < min(end, t.Segment.Stop+1); i++ {
									block[i-start] = ' '
								}
							}
						}
						return ast.WalkSkipChildren, nil
					}
					return ast.WalkContinue, nil
				})
				// Only leaf inline containers; list/quote parents must not rescan children.
				if node.FirstChild().Type() == ast.TypeInline {
					raw := string(block)
					for _, m := range wiki.FindAllStringSubmatchIndex(raw, -1) {
						if m[0] > 0 && raw[m[0]-1] == '\\' {
							continue
						}
						scanWiki(raw[m[0]:m[1]], &result.Links)
					}
					for _, m := range hashtag.FindAllStringSubmatchIndex(raw, -1) {
						tag := raw[m[4]:m[5]]
						result.Tags = append(result.Tags, tag)
					}
				}
			}
		}
		return ast.WalkContinue, nil
	})
	result.Aliases = unique(result.Aliases)
	for i, v := range result.Tags {
		result.Tags[i] = strings.TrimPrefix(v, "#")
	}
	result.Tags = unique(result.Tags)
	result.Properties = unique(result.Properties)
	sort.Strings(result.Properties)
	seen := map[Reference]bool{}
	links := []Reference{}
	for _, link := range result.Links {
		if link.Target != "" && !seen[link] {
			seen[link] = true
			links = append(links, link)
		}
	}
	result.Links = links
	return result
}

func notePath(value string) bool {
	value = strings.ToLower(value)
	return strings.HasSuffix(value, ".md") || strings.HasSuffix(value, ".markdown")
}
func stem(value string) string {
	name := path.Base(value)
	if notePath(name) {
		return name[:len(name)-len(path.Ext(name))]
	}
	return name
}
func referenceTarget(value string) (string, string, error) {
	value = strings.TrimSpace(strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(value, "[["), "]]"), "|", 2)[0])
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return "", "", err
	}
	value = decoded
	if strings.ContainsAny(value, "\\\x00\r\n") || strings.Contains(value, ":") {
		return "", "", fmt.Errorf("external or invalid note reference")
	}
	parts := strings.SplitN(value, "#", 2)
	heading := ""
	if len(parts) > 1 {
		heading = parts[1]
	}
	return parts[0], heading, nil
}
func documentID(root, relative string) string {
	sum := sha256.Sum256([]byte(root + "\x00" + relative))
	return "workspace_note_" + hex.EncodeToString(sum[:16])
}
