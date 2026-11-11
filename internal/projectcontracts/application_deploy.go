package projectcontracts

import "strings"

// The resource is part of the exact existing operation effect, so a targeted
// deploy cannot accidentally enroll knowledge or resume an unrelated clock.
func ApplicationDeployEffect(key ResourceKey) DeclarationEffect {
	return DeclarationEffect("deploy_application:" + string(key))
}

func ApplicationDeployResource(effects []DeclarationEffect) (ResourceKey, bool) {
	if len(effects) != 1 || !strings.HasPrefix(string(effects[0]), "deploy_application:") {
		return "", false
	}
	key := strings.TrimPrefix(string(effects[0]), "deploy_application:")
	if !declarationKey(key) {
		return "", false
	}
	return ResourceKey(key), true
}
