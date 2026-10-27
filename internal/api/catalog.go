package api

import "encoding/json"

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

// HasCapability accepts only explicit true values, never provider/model-name
// heuristics. Unknown, null and future structured values do not match.
func (m Model) HasCapability(name string) bool {
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(m.Metadata["capabilities"], &capabilities) != nil {
		return false
	}
	return string(capabilities[name]) == "true"
}
