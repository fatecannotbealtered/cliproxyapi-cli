package cmd

import (
	"reflect"
	"testing"
)

func assertRuntimeUntrustedMatchesReference(t *testing.T, schemaName string, data map[string]any) {
	t.Helper()
	runtimeFields := stringSliceFromAny(t, data["_untrusted"])

	exit, stdout, stderr := runCommand(t, "reference", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("reference exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	referenceData := decodeEnvelope(t, stdout)["data"].(map[string]any)
	schemas := referenceData["schemas"].(map[string]any)
	schema := schemas[schemaName].(map[string]any)
	referenceFields := stringSliceFromAny(t, schema["untrusted_fields"])
	if !reflect.DeepEqual(runtimeFields, referenceFields) {
		t.Fatalf("runtime _untrusted = %#v, reference %s = %#v", runtimeFields, schemaName, referenceFields)
	}
}

func assertRuntimeUntrustedCoveredByReference(t *testing.T, schemaName string, data map[string]any) {
	t.Helper()
	runtimeFields := stringSliceFromAny(t, data["_untrusted"])

	exit, stdout, stderr := runCommand(t, "reference", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("reference exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	referenceData := decodeEnvelope(t, stdout)["data"].(map[string]any)
	schemas := referenceData["schemas"].(map[string]any)
	schema := schemas[schemaName].(map[string]any)
	referenceFields := stringSliceFromAny(t, schema["untrusted_fields"])
	covered := make(map[string]bool, len(referenceFields))
	for _, field := range referenceFields {
		covered[field] = true
	}
	for _, field := range runtimeFields {
		if !covered[field] {
			t.Fatalf("runtime _untrusted field %q is absent from reference %s = %#v", field, schemaName, referenceFields)
		}
	}
}
