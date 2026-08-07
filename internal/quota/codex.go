// Package quota conservatively classifies upstream provider quota responses.
package quota

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"time"
)

type State string

const (
	StateHealthy            State = "healthy"
	StateConfirmedExhausted State = "confirmed_exhausted"
	StateUnknown            State = "unknown"
)

type Window struct {
	Name               string     `json:"name"`
	UsedPercent        *float64   `json:"used_percent,omitempty"`
	LimitWindowSeconds *float64   `json:"limit_window_seconds,omitempty"`
	ResetAt            *time.Time `json:"reset_at,omitempty"`
}

type Assessment struct {
	State       State          `json:"state"`
	Reason      string         `json:"reason"`
	UsedPercent *float64       `json:"used_percent,omitempty"`
	ResetAt     *time.Time     `json:"reset_at,omitempty"`
	Windows     []Window       `json:"windows"`
	Evidence    map[string]any `json:"evidence"`
	Untrusted   []string       `json:"_untrusted,omitempty"`
}

type parsedWindow struct {
	window     Window
	recognized bool
	invalid    bool
	exhausted  bool
}

type parsedRateLimit struct {
	windows       []parsedWindow
	recognized    bool
	invalid       bool
	globalSignal  bool
	windowSignals bool
	firstReason   string
	evidence      map[string]any
}

type parsedUsageSignals struct {
	accountExhausted    bool
	additionalExhausted bool
	invalid             bool
	firstReason         string
	resetAt             *time.Time
	evidence            map[string]any
}

// AssessCodex classifies a Codex/ChatGPT wham/usage response. Unknown or
// malformed responses deliberately fail closed: they never report exhaustion.
func AssessCodex(statusCode int, body []byte, now time.Time) Assessment {
	result := Assessment{
		State:     StateUnknown,
		Reason:    "response does not contain a recognized quota signal",
		Windows:   []Window{},
		Evidence:  map[string]any{"status_code": statusCode},
		Untrusted: []string{},
	}

	root, err := decodeObject(body)
	if err != nil {
		result.Reason = "response body is not a JSON object"
		return result
	}

	rate := parseRateLimit(root, now)
	for _, parsed := range rate.windows {
		result.Windows = append(result.Windows, parsed.window)
	}
	if len(rate.evidence) > 0 {
		result.Evidence["rate_limit"] = rate.evidence
	}
	usage := parseUsageSignals(root, now)
	for key, value := range usage.evidence {
		result.Evidence[key] = value
	}

	errorReason, errorEvidence, untrusted := parseStructuredError(root)
	if len(errorEvidence) > 0 {
		result.Evidence["error"] = errorEvidence
		result.Untrusted = append(result.Untrusted, untrusted...)
	}

	if rate.invalid || usage.invalid {
		result.Reason = "known quota fields are malformed or ambiguous"
		return result
	}

	if usage.additionalExhausted {
		result.Reason = "additional_rate_limits reports feature/model exhaustion; account-wide quota state is unknown"
		return result
	}

	if rate.globalSignal || rate.windowSignals || usage.accountExhausted || errorReason != "" {
		result.State = StateConfirmedExhausted
		result.Reason = rate.firstReason
		if result.Reason == "" {
			result.Reason = usage.firstReason
		}
		if result.Reason == "" {
			result.Reason = errorReason
		}
		result.UsedPercent = highestExhaustedPercent(rate.windows)
		result.ResetAt = exhaustionReset(rate)
		if usage.resetAt != nil && (result.ResetAt == nil || usage.resetAt.Before(*result.ResetAt)) {
			result.ResetAt = timePointer(*usage.resetAt)
		}
		return result
	}

	if statusCode >= 200 && statusCode < 300 && rate.recognized {
		result.State = StateHealthy
		result.Reason = "recognized rate-limit windows are below exhaustion"
		result.UsedPercent = highestKnownPercent(rate.windows)
		return result
	}

	if statusCode < 200 || statusCode >= 300 {
		result.Reason = fmt.Sprintf("HTTP status %d has no explicit quota-exhaustion signal", statusCode)
	}
	return result
}

func decodeObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON root is not an object")
	}
	return root, nil
}

func parseRateLimit(root map[string]any, now time.Time) parsedRateLimit {
	parsed := parsedRateLimit{evidence: map[string]any{}}
	value, present, valid := lookupAlias(root, "rate_limit", "rateLimit")
	if !present {
		return parsed
	}
	if !valid {
		parsed.invalid = true
		return parsed
	}
	if value == nil {
		return parsed
	}
	rate, ok := value.(map[string]any)
	if !ok {
		parsed.invalid = true
		return parsed
	}

	if allowed, present, valid := boolField(rate, "allowed", "allowed"); present {
		if !valid {
			parsed.invalid = true
		} else {
			parsed.evidence["allowed"] = allowed
			if !allowed {
				parsed.globalSignal = true
				parsed.firstReason = "rate_limit.allowed is false"
			}
		}
	}
	if reached, present, valid := boolField(rate, "limit_reached", "limitReached"); present {
		if !valid {
			parsed.invalid = true
		} else {
			parsed.evidence["limit_reached"] = reached
			if reached {
				parsed.globalSignal = true
				if parsed.firstReason == "" {
					parsed.firstReason = "rate_limit.limit_reached is true"
				}
			}
		}
	}

	for _, definition := range []struct {
		name  string
		snake string
		camel string
	}{
		{name: "primary", snake: "primary_window", camel: "primaryWindow"},
		{name: "secondary", snake: "secondary_window", camel: "secondaryWindow"},
	} {
		windowValue, present, valid := lookupAlias(rate, definition.snake, definition.camel)
		if !present {
			continue
		}
		if !valid {
			parsed.invalid = true
			continue
		}
		if windowValue == nil {
			continue
		}
		windowObject, ok := windowValue.(map[string]any)
		if !ok {
			parsed.invalid = true
			continue
		}

		window := parseWindow(definition.name, windowObject, now)
		parsed.invalid = parsed.invalid || window.invalid
		if !window.recognized {
			continue
		}
		if window.window.UsedPercent != nil {
			parsed.recognized = true
		}
		parsed.windows = append(parsed.windows, window)
		parsed.evidence[definition.snake] = windowEvidence(window.window)
		if window.exhausted {
			parsed.windowSignals = true
			if parsed.firstReason == "" {
				parsed.firstReason = "rate_limit." + definition.snake + ".used_percent is at least 100"
			}
		}
	}

	return parsed
}

func parseUsageSignals(root map[string]any, now time.Time) parsedUsageSignals {
	parsed := parsedUsageSignals{evidence: map[string]any{}}

	if value, present, valid := lookupAlias(root, "rate_limit_reached_type", "rateLimitReachedType"); present {
		if !valid {
			parsed.invalid = true
		} else if value != nil {
			object, ok := value.(map[string]any)
			if !ok {
				parsed.invalid = true
			} else {
				kind, ok := object["type"].(string)
				if !ok {
					parsed.invalid = true
				} else {
					parsed.evidence["rate_limit_reached_type"] = map[string]any{"type": kind}
					if isAccountExhaustionType(kind) {
						parsed.accountExhausted = true
						parsed.firstReason = "rate_limit_reached_type.type explicitly reports account exhaustion"
					} else {
						parsed.invalid = true
					}
				}
			}
		}
	}

	if value, present, valid := lookupAlias(root, "spend_control", "spendControl"); present {
		if !valid {
			parsed.invalid = true
		} else if value != nil {
			object, ok := value.(map[string]any)
			if !ok {
				parsed.invalid = true
			} else {
				evidence := map[string]any{}
				reached, reachedPresent, reachedValid := boolField(object, "reached", "reached")
				if !reachedPresent || !reachedValid {
					parsed.invalid = true
				} else {
					evidence["reached"] = reached
				}

				limitValue, limitPresent, limitValid := lookupAlias(object, "individual_limit", "individualLimit")
				if !limitValid {
					parsed.invalid = true
				} else if limitPresent && limitValue != nil {
					limit, ok := limitValue.(map[string]any)
					if !ok {
						parsed.invalid = true
					} else {
						resetAt, resetEvidence, invalid := parseResetFields(limit, now)
						parsed.invalid = parsed.invalid || invalid
						if len(resetEvidence) > 0 {
							evidence["individual_limit"] = resetEvidence
						}
						if reached && resetAt != nil {
							parsed.resetAt = resetAt
						}
					}
				}
				if len(evidence) > 0 {
					parsed.evidence["spend_control"] = evidence
				}
				if reachedPresent && reachedValid && reached {
					parsed.accountExhausted = true
					if parsed.firstReason == "" {
						parsed.firstReason = "spend_control.reached is true"
					}
				}
			}
		}
	}

	if value, present, valid := lookupAlias(root, "credits", "credits"); present {
		if !valid {
			parsed.invalid = true
		} else if value != nil {
			object, ok := value.(map[string]any)
			if !ok {
				parsed.invalid = true
			} else {
				hasCredits, fieldPresent, fieldValid := boolField(object, "has_credits", "hasCredits")
				if !fieldPresent || !fieldValid {
					parsed.invalid = true
				} else {
					parsed.evidence["credits"] = map[string]any{"has_credits": hasCredits}
				}
			}
		}
	}

	if value, present, valid := lookupAlias(root, "additional_rate_limits", "additionalRateLimits"); present {
		if !valid {
			parsed.invalid = true
		} else if value != nil {
			items, ok := value.([]any)
			if !ok {
				parsed.invalid = true
			} else {
				evidence := make([]map[string]any, 0, len(items))
				for _, value := range items {
					itemEvidence := map[string]any{}
					item, ok := value.(map[string]any)
					if !ok {
						parsed.invalid = true
						evidence = append(evidence, itemEvidence)
						continue
					}
					for _, field := range []struct {
						snake string
						camel string
					}{
						{snake: "limit_name", camel: "limitName"},
						{snake: "metered_feature", camel: "meteredFeature"},
					} {
						fieldValue, fieldPresent, fieldValid := lookupAlias(item, field.snake, field.camel)
						if !fieldPresent {
							continue
						}
						text, ok := fieldValue.(string)
						if !fieldValid || !ok {
							parsed.invalid = true
							continue
						}
						itemEvidence[field.snake] = text
					}

					limit := parseRateLimit(item, now)
					parsed.invalid = parsed.invalid || limit.invalid
					if len(limit.evidence) > 0 {
						itemEvidence["rate_limit"] = limit.evidence
					}
					if limit.globalSignal || limit.windowSignals {
						parsed.additionalExhausted = true
					}
					evidence = append(evidence, itemEvidence)
				}
				parsed.evidence["additional_rate_limits"] = evidence
			}
		}
	}

	return parsed
}

func isAccountExhaustionType(kind string) bool {
	switch kind {
	case "rate_limit_reached",
		"workspace_owner_credits_depleted",
		"workspace_member_credits_depleted",
		"workspace_owner_usage_limit_reached",
		"workspace_member_usage_limit_reached":
		return true
	default:
		return false
	}
}

func parseResetFields(object map[string]any, now time.Time) (*time.Time, map[string]any, bool) {
	evidence := map[string]any{}
	invalid := false
	var absoluteReset, relativeReset *time.Time

	value, present, valid := lookupAlias(object, "reset_at", "resetAt")
	if present {
		if !valid {
			invalid = true
		} else if resetAt, ok := parseResetAt(value); ok {
			absoluteReset = timePointer(resetAt)
			evidence["reset_at"] = resetAt.Format(time.RFC3339Nano)
		} else {
			invalid = true
		}
	}

	after, present, valid := numberField(object, "reset_after_seconds", "resetAfterSeconds")
	if present {
		if !valid || after < 0 || after > float64(math.MaxInt64)/float64(time.Second) {
			invalid = true
		} else {
			resetAt := now.Add(time.Duration(math.Round(after * float64(time.Second)))).UTC()
			relativeReset = timePointer(resetAt)
			evidence["reset_after_seconds"] = after
		}
	}

	if absoluteReset != nil {
		return absoluteReset, evidence, invalid
	}
	return relativeReset, evidence, invalid
}

func parseWindow(name string, object map[string]any, now time.Time) parsedWindow {
	parsed := parsedWindow{window: Window{Name: name}}

	if value, present, valid := numberField(object, "used_percent", "usedPercent"); present {
		if !valid || value < 0 {
			parsed.invalid = true
		} else {
			parsed.recognized = true
			parsed.window.UsedPercent = floatPointer(value)
			parsed.exhausted = value >= 100
		}
	}
	if value, present, valid := numberField(object, "limit_window_seconds", "limitWindowSeconds"); present {
		if !valid || value < 0 {
			parsed.invalid = true
		} else {
			parsed.recognized = true
			parsed.window.LimitWindowSeconds = floatPointer(value)
		}
	}

	resetAt, _, invalid := parseResetFields(object, now)
	parsed.invalid = parsed.invalid || invalid
	if resetAt != nil {
		parsed.recognized = true
		parsed.window.ResetAt = timePointer(*resetAt)
	}

	return parsed
}

func parseStructuredError(root map[string]any) (string, map[string]any, []string) {
	value, ok := root["error"]
	if !ok {
		return "", nil, nil
	}
	errorObject, ok := value.(map[string]any)
	if !ok {
		return "", nil, nil
	}

	evidence := map[string]any{}
	untrusted := []string{}
	reason := ""
	for _, field := range []string{"code", "type"} {
		value, ok := errorObject[field].(string)
		if !ok || (value != "usage_limit_reached" && value != "quota_exhausted") {
			continue
		}
		evidence[field] = value
		untrusted = append(untrusted, "evidence.error."+field)
		if reason == "" {
			reason = "error." + field + " explicitly reports quota exhaustion"
		}
	}
	return reason, evidence, untrusted
}

func windowEvidence(window Window) map[string]any {
	evidence := map[string]any{}
	if window.UsedPercent != nil {
		evidence["used_percent"] = *window.UsedPercent
	}
	if window.LimitWindowSeconds != nil {
		evidence["limit_window_seconds"] = *window.LimitWindowSeconds
	}
	if window.ResetAt != nil {
		evidence["reset_at"] = window.ResetAt.UTC().Format(time.RFC3339Nano)
	}
	return evidence
}

func exhaustionReset(rate parsedRateLimit) *time.Time {
	candidates := make([]time.Time, 0, len(rate.windows))
	for _, window := range rate.windows {
		if window.exhausted && window.window.ResetAt != nil {
			candidates = append(candidates, *window.window.ResetAt)
		}
	}
	if len(candidates) == 0 && rate.globalSignal {
		for _, window := range rate.windows {
			if window.window.ResetAt != nil {
				candidates = append(candidates, *window.window.ResetAt)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	earliest := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Before(earliest) {
			earliest = candidate
		}
	}
	return timePointer(earliest)
}

func highestExhaustedPercent(windows []parsedWindow) *float64 {
	var highest *float64
	for _, window := range windows {
		if !window.exhausted || window.window.UsedPercent == nil {
			continue
		}
		if highest == nil || *window.window.UsedPercent > *highest {
			highest = floatPointer(*window.window.UsedPercent)
		}
	}
	return highest
}

func highestKnownPercent(windows []parsedWindow) *float64 {
	var highest *float64
	for _, window := range windows {
		if window.window.UsedPercent == nil {
			continue
		}
		if highest == nil || *window.window.UsedPercent > *highest {
			highest = floatPointer(*window.window.UsedPercent)
		}
	}
	return highest
}

func boolField(object map[string]any, snake, camel string) (bool, bool, bool) {
	value, present, valid := lookupAlias(object, snake, camel)
	if !present || !valid {
		return false, present, valid
	}
	boolean, ok := value.(bool)
	return boolean, true, ok
}

func numberField(object map[string]any, snake, camel string) (float64, bool, bool) {
	value, present, valid := lookupAlias(object, snake, camel)
	if !present || !valid {
		return 0, present, valid
	}
	number, ok := numberValue(value)
	return number, true, ok
}

func numberValue(value any) (float64, bool) {
	var number float64
	var err error
	switch value := value.(type) {
	case json.Number:
		number, err = value.Float64()
	case float64:
		number = value
	default:
		return 0, false
	}
	return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func parseResetAt(value any) (time.Time, bool) {
	if number, ok := numberValue(value); ok {
		return unixSeconds(number)
	}
	text, ok := value.(string)
	if !ok {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return parsed.UTC(), true
	}
	if number, err := strconv.ParseFloat(text, 64); err == nil {
		return unixSeconds(number)
	}
	return time.Time{}, false
}

func unixSeconds(value float64) (time.Time, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < -62135596800 || value > 253402300799 {
		return time.Time{}, false
	}
	seconds, fraction := math.Modf(value)
	nanoseconds := int64(math.Round(fraction * float64(time.Second)))
	return time.Unix(int64(seconds), nanoseconds).UTC(), true
}

func lookupAlias(object map[string]any, snake, camel string) (any, bool, bool) {
	snakeValue, snakePresent := object[snake]
	if snake == camel {
		return snakeValue, snakePresent, true
	}
	camelValue, camelPresent := object[camel]
	if !snakePresent {
		return camelValue, camelPresent, true
	}
	if !camelPresent {
		return snakeValue, true, true
	}
	if !reflect.DeepEqual(snakeValue, camelValue) {
		return nil, true, false
	}
	return snakeValue, true, true
}

func floatPointer(value float64) *float64 {
	return &value
}

func timePointer(value time.Time) *time.Time {
	return &value
}
