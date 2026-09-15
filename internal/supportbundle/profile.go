package supportbundle

import (
	"fmt"
	"strings"
)

type Profile string

const (
	ProfileMinimal Profile = "minimal"
	ProfileDefault Profile = "default"
	ProfileFull    Profile = "full"
)

func NormalizeProfile(value string) (Profile, error) {
	switch Profile(strings.ToLower(strings.TrimSpace(value))) {
	case "", ProfileDefault:
		return ProfileDefault, nil
	case ProfileMinimal:
		return ProfileMinimal, nil
	case ProfileFull:
		return ProfileFull, nil
	default:
		return "", fmt.Errorf("unsupported support bundle profile %q", value)
	}
}

func profileAllows(profile Profile, allowed []Profile) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if profile == candidate {
			return true
		}
	}
	return false
}
