package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Preserve unknown catalog fields in JSON output, including future capability
// objects, numeric prices and context pricing tiers. Known legacy fields retain
// their original names and output shape.
func (m *Model) UnmarshalJSON(data []byte) error {
	type legacy Model
	var known legacy
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"id", "object", "owned_by", "context", "our_price", "endpoint", "supported_endpoints"} {
		delete(fields, key)
	}
	*m = Model(known)
	m.Metadata = fields
	return nil
}

func (m Model) MarshalJSON() ([]byte, error) {
	type legacy Model
	data, err := json.Marshal(legacy(m))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range m.Metadata {
		if _, exists := fields[key]; !exists {
			fields[key] = value
		}
	}
	return json.Marshal(fields)
}

// CapabilityState is the tri-state value of the versioned catalog contract
// (capabilities_version 1): capabilities[<public endpoint>].<feature>.
type CapabilityState string

const (
	CapabilitySupported   CapabilityState = "supported"
	CapabilityUnsupported CapabilityState = "unsupported"
	CapabilityUnknown     CapabilityState = "unknown"
)

// CapabilitiesVersion is the only catalog capability schema this client interprets.
const CapabilitiesVersion = 1

var capabilityPathPattern = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)

// NormalizeEndpoint maps "responses", "/responses", "v1/responses" and
// "/v1/responses" to the public catalog key "/v1/responses".
func NormalizeEndpoint(endpoint string) string {
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	endpoint = strings.TrimPrefix(endpoint, "v1/")
	if endpoint == "" || endpoint == "v1" {
		return ""
	}
	return "/v1/" + endpoint
}

// ValidateCapabilityPath accepts dotted tri-state feature paths such as
// tool_calling, structured_outputs.json_schema or modalities.input.image.
// Groups, numeric limits, option lists and access metadata are not features.
func ValidateCapabilityPath(feature string) error {
	if !capabilityPathPattern.MatchString(feature) {
		return fmt.Errorf("capability must be a lowercase dotted feature path, e.g. tool_calling or modalities.input.image")
	}
	switch feature {
	case "structured_outputs":
		return fmt.Errorf("structured_outputs is a group; use structured_outputs.json_schema")
	case "modalities", "modalities.input", "modalities.output":
		return fmt.Errorf("%s is a group; use e.g. modalities.input.image or modalities.output.text", feature)
	case "reasoning":
		return fmt.Errorf("reasoning is a group; use reasoning.supported")
	case "max_output_tokens", "output_token_limit":
		return fmt.Errorf("%s is a numeric limit, not a supported/unsupported feature; inspect it with models show --json", feature)
	}
	if feature == "access" || strings.HasPrefix(feature, "access.") || feature == "reasoning.options" || strings.HasPrefix(feature, "reasoning.options.") {
		return fmt.Errorf("%s is not a tri-state feature; inspect it with models show --json", feature)
	}
	return nil
}

// Capability reads capabilities[endpoint].<feature> for capabilities_version 1.
// Missing, null, malformed or differently versioned declarations are unknown;
// another endpoint's declaration is never reused and nothing is inferred from
// model names, providers or prices.
func (m Model) Capability(endpoint, feature string) CapabilityState {
	var version float64
	if json.Unmarshal(m.Metadata["capabilities_version"], &version) != nil || version != CapabilitiesVersion {
		return CapabilityUnknown
	}
	var endpoints map[string]json.RawMessage
	if json.Unmarshal(m.Metadata["capabilities"], &endpoints) != nil {
		return CapabilityUnknown
	}
	want := NormalizeEndpoint(endpoint)
	if want == "" {
		return CapabilityUnknown
	}
	result := CapabilityState("")
	for key, raw := range endpoints {
		if NormalizeEndpoint(key) != want {
			continue
		}
		state := lookupCapability(raw, strings.Split(feature, "."))
		if result != "" && result != state {
			return CapabilityUnknown // conflicting aliases of the same endpoint
		}
		result = state
	}
	if result == "" {
		return CapabilityUnknown
	}
	return result
}

func lookupCapability(raw json.RawMessage, path []string) CapabilityState {
	for _, segment := range path {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return CapabilityUnknown
		}
		raw = object[segment]
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return CapabilityUnknown
	}
	switch state := CapabilityState(value); state {
	case CapabilitySupported, CapabilityUnsupported:
		return state
	}
	return CapabilityUnknown
}

// DeclaresEndpoint reports whether the catalog lists a route for the endpoint
// in endpoint, supported_endpoints or as a key of the capability map.
func (m Model) DeclaresEndpoint(endpoint string) bool {
	want := NormalizeEndpoint(endpoint)
	if want == "" {
		return false
	}
	if NormalizeEndpoint(m.Endpoint) == want {
		return true
	}
	for _, e := range m.SupportedEndpoints {
		if NormalizeEndpoint(e) == want {
			return true
		}
	}
	var endpoints map[string]json.RawMessage
	if json.Unmarshal(m.Metadata["capabilities"], &endpoints) == nil {
		for key := range endpoints {
			if NormalizeEndpoint(key) == want {
				return true
			}
		}
	}
	return false
}
