package backupcontracts

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

func Parse(payload []byte) (Contract, error) {
	var contract Contract
	if err := yaml.Unmarshal(payload, &contract); err != nil {
		return Contract{}, fmt.Errorf("parse backup contract yaml: %w", err)
	}
	contract = Normalize(contract)
	if err := Validate(contract); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func Render(contract Contract) ([]byte, error) {
	contract = Normalize(contract)
	if err := Validate(contract); err != nil {
		return nil, err
	}
	payload, err := yaml.Marshal(contract)
	if err != nil {
		return nil, fmt.Errorf("render backup contract yaml: %w", err)
	}
	return payload, nil
}
