package setup

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func ManifestSensitiveKeys(path string) []string {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var decoded any
	if err := yaml.Unmarshal(payload, &decoded); err != nil {
		return nil
	}
	keys := []string{}
	collectSensitiveKeys(decoded, "", &keys)
	return keys
}

func collectSensitiveKeys(value any, prefix string, keys *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if IsSensitiveKey(key) && !isRedactedOrEmpty(item) {
				*keys = append(*keys, path)
			}
			collectSensitiveKeys(item, path, keys)
		}
	case map[any]any:
		for rawKey, item := range typed {
			key, ok := rawKey.(string)
			if !ok {
				continue
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if IsSensitiveKey(key) && !isRedactedOrEmpty(item) {
				*keys = append(*keys, path)
			}
			collectSensitiveKeys(item, path, keys)
		}
	case []any:
		for _, item := range typed {
			collectSensitiveKeys(item, prefix, keys)
		}
	}
}

func isRedactedOrEmpty(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	text = strings.TrimSpace(text)
	return text == "" || text == "[REDACTED]"
}
