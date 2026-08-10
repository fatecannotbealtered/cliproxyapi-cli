package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

type canonicalContract struct {
	Envelope struct {
		SchemaVersionValue string   `json:"schema_version_value"`
		SuccessKeys        []string `json:"success_keys"`
		ErrorKeys          []string `json:"error_keys"`
		ErrorObjectKeys    []string `json:"error_object_keys"`
		MetaRequiredKeys   []string `json:"meta_required_keys"`
		MetaOptionalKeys   []string `json:"meta_optional_keys"`
	} `json:"envelope"`
	ErrorCodes struct {
		Core map[string]struct {
			Exit      int  `json:"exit"`
			Retryable bool `json:"retryable"`
		} `json:"core"`
	} `json:"error_codes"`
	ExitCodes struct {
		Table map[string]string `json:"table"`
	} `json:"exit_codes"`
	SelfDescription struct {
		Reference struct {
			RequiredTopKeys              []string `json:"required_top_keys"`
			ReleaseReadinessRequiredKeys []string `json:"release_readiness_required_keys"`
		} `json:"reference"`
		Context struct {
			RequiredTopKeys []string `json:"required_top_keys"`
		} `json:"context"`
		Doctor struct {
			CheckItemRequiredKeys []string `json:"check_item_required_keys"`
		} `json:"doctor"`
		Changelog struct {
			RequiredDataKeys []string `json:"required_data_keys"`
		} `json:"changelog"`
		Update struct {
			RequiredKeysOnAvailable []string `json:"required_keys_on_available"`
		} `json:"update"`
	} `json:"self_description"`
}

func TestCommandsConformToCanonicalContract(t *testing.T) {
	contract := readCanonicalContract(t)
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", t.TempDir())
	t.Setenv("CLIPROXYAPI_CLI_BASE_URL", "")
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "")

	contextData := runConformantSuccess(t, contract, "context", "--compact")
	for _, key := range contract.SelfDescription.Context.RequiredTopKeys {
		if key == "notices" {
			continue // Optional in practice: the canonical note requires omission when empty.
		}
		assertRequiredKeys(t, contextData, []string{key})
	}

	doctorData := runConformantSuccess(t, contract, "doctor", "--compact")
	checks, ok := doctorData["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("doctor checks = %#v", doctorData["checks"])
	}
	for _, raw := range checks {
		assertRequiredKeys(t, raw.(map[string]any), contract.SelfDescription.Doctor.CheckItemRequiredKeys)
	}

	changelogData := runConformantSuccess(t, contract, "changelog", "--compact")
	assertRequiredKeys(t, changelogData, contract.SelfDescription.Changelog.RequiredDataKeys)

	updateServer := updateMockReleaseServer(t, version, true)
	defer updateServer.Close()
	withUpdateServer(t, updateServer)
	updateData := runConformantSuccess(t, contract, "update", "--check", "--compact")
	assertRequiredKeys(t, updateData, contract.SelfDescription.Update.RequiredKeysOnAvailable)

	referenceData := runConformantSuccess(t, contract, "reference", "--compact")
	assertRequiredKeys(t, referenceData, contract.SelfDescription.Reference.RequiredTopKeys)
	readiness := referenceData["release_readiness"].(map[string]any)
	assertRequiredKeys(t, readiness, contract.SelfDescription.Reference.ReleaseReadinessRequiredKeys)
	assertReferenceTables(t, referenceData, contract)

	exit, stdout, stderr := runCommand(t, "unknown-command", "--compact")
	if stderr != "" {
		t.Fatalf("failure stderr = %q", stderr)
	}
	envelope := decodeEnvelope(t, stdout)
	assertEnvelope(t, envelope, false, contract)
	errorObject := envelope["error"].(map[string]any)
	code := errorObject["code"].(string)
	spec, ok := contract.ErrorCodes.Core[code]
	if !ok || exit != spec.Exit || errorObject["retryable"] != spec.Retryable {
		t.Fatalf("error triple = code %q exit %d retryable %#v", code, exit, errorObject["retryable"])
	}
}

func readCanonicalContract(t *testing.T) canonicalContract {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fccModuleRoot(), "contract", "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract canonicalContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	return contract
}

func runConformantSuccess(t *testing.T, contract canonicalContract, args ...string) map[string]any {
	t.Helper()
	exit, stdout, stderr := runCommand(t, args...)
	if exit != 0 || stderr != "" {
		t.Fatalf("%v: exit=%d stderr=%q stdout=%s", args, exit, stderr, stdout)
	}
	envelope := decodeEnvelope(t, stdout)
	assertEnvelope(t, envelope, true, contract)
	return envelope["data"].(map[string]any)
}

func assertEnvelope(t *testing.T, envelope map[string]any, success bool, contract canonicalContract) {
	t.Helper()
	wantKeys := contract.Envelope.ErrorKeys
	if success {
		wantKeys = contract.Envelope.SuccessKeys
	}
	assertExactKeys(t, envelope, wantKeys)
	if envelope["ok"] != success || envelope["schema_version"] != contract.Envelope.SchemaVersionValue {
		t.Fatalf("envelope header = %#v", envelope)
	}
	meta := envelope["meta"].(map[string]any)
	assertAllowedKeys(t, meta, contract.Envelope.MetaRequiredKeys, contract.Envelope.MetaOptionalKeys)
	if !success {
		assertExactKeys(t, envelope["error"].(map[string]any), contract.Envelope.ErrorObjectKeys)
	}
}

func assertReferenceTables(t *testing.T, data map[string]any, contract canonicalContract) {
	t.Helper()
	exitCodes := data["exit_codes"].(map[string]any)
	if len(exitCodes) != len(contract.ExitCodes.Table) {
		t.Fatalf("reference exit_codes count = %d, want %d", len(exitCodes), len(contract.ExitCodes.Table))
	}
	for code, meaning := range contract.ExitCodes.Table {
		if exitCodes[code] != meaning {
			t.Errorf("reference exit_codes[%s] = %#v, want %q", code, exitCodes[code], meaning)
		}
	}
	errorCodes := data["error_codes"].(map[string]any)
	if len(errorCodes) != len(contract.ErrorCodes.Core) {
		t.Fatalf("reference error_codes count = %d, want %d", len(errorCodes), len(contract.ErrorCodes.Core))
	}
	for code, spec := range contract.ErrorCodes.Core {
		actual := errorCodes[code].(map[string]any)
		if actual["exit"] != float64(spec.Exit) || actual["retryable"] != spec.Retryable {
			t.Errorf("reference error_codes[%s] = %#v", code, actual)
		}
	}
}

func assertRequiredKeys(t *testing.T, object map[string]any, required []string) {
	t.Helper()
	for _, key := range required {
		if _, ok := object[key]; !ok {
			t.Errorf("required key %q missing from %#v", key, object)
		}
	}
}

func assertExactKeys(t *testing.T, object map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func assertAllowedKeys(t *testing.T, object map[string]any, required, optional []string) {
	t.Helper()
	assertRequiredKeys(t, object, required)
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range append(required, optional...) {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			t.Errorf("unexpected key %q in %#v", key, object)
		}
	}
}
