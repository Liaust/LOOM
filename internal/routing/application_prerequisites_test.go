package routing

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONValueExactSemanticEquality(t *testing.T) {
	for _, tc := range []struct {
		name, a, b string
		equal      bool
	}{
		{"object", `{"a":null,"b":[true,1,"1"]}`, ` { "b": [true, 1.00e0, "1"], "a": null } `, true},
		{"array_order", `[1,2]`, `[2,1]`, false},
		{"missing_null", `{"a":null}`, `{}`, false},
		{"type", `"1"`, `1`, false},
		{"bool", `true`, `false`, false},
		{"null", `null`, `null`, true},
		{"uint64", `{"n":18446744073709551615}`, `{"n":18446744073709551614}`, false},
		{"int64", `-9223372036854775808`, `-9223372036854775807`, false},
		{"adjacent", `9007199254740992`, `9007199254740993`, false},
		{"uint_spelling", `18446744073709551615`, `184467440737095516150e-1`, true},
		{"negative_zero", `-0.0e10000000000`, `0`, true},
		{"decimal", `12.3400`, `1234e-2`, true},
		{"sign", `-12.34`, `12.34`, false},
		{"huge_exponent", `1e100000000000000000000`, `10e99999999999999999999`, true},
		{"huge_negative", `1e-100000000000000000000`, `0.1e-99999999999999999999`, true},
		{"huge_distinct", `1e100000000000000000000`, `1e100000000000000000001`, false},
		{"malformed", `{`, `{`, false},
		{"documents", `{} {}`, `{} {}`, false},
		{"trailing", `{} x`, `{}`, false},
		{"exponent_work_bound", "1e" + strings.Repeat("9", 1025), "1e" + strings.Repeat("9", 1025), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameJSONValue([]byte(tc.a), json.RawMessage(tc.b)); got != tc.equal {
				t.Fatalf("equality=%v want %v", got, tc.equal)
			}
			if got := sameJSONValue([]byte(tc.b), json.RawMessage(tc.a)); got != tc.equal {
				t.Fatal("not symmetric")
			}
		})
	}
}

func TestApplicationPrerequisiteResultPredicatePreservesGenericEndpoints(t *testing.T) {
	for name, accepted := range map[string]bool{
		"workspace/node@system.project.application.prerequisites":            true,
		"capability:workspace/node@system.project.application.prerequisites": true,
		"workspace/node@custom.project.application.prerequisites":            false,
		"main@system.project.application.prerequisites":                      false,
		"workspace/node@system.project.application.inspect":                  false,
		"invalid": false,
	} {
		t.Run(name, func(t *testing.T) {
			if isApplicationPrerequisiteOperation(name) != accepted {
				t.Fatal("wrong result-owner classification")
			}
		})
	}
}
