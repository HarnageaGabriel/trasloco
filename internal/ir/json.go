package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type pipelineJSON Pipeline

// MarshalJSON refuses to serialize missing or unsupported schema versions.
func (p Pipeline) MarshalJSON() ([]byte, error) {
	if p.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("IR schemaVersion = %d, want %d", p.SchemaVersion, SchemaVersion)
	}
	return json.Marshal(pipelineJSON(p))
}

// UnmarshalJSON requires the current schema and rejects unknown fields.
func (p *Pipeline) UnmarshalJSON(data []byte) error {
	var header struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return fmt.Errorf("decode IR schemaVersion: %w", err)
	}
	if header.SchemaVersion == nil {
		return fmt.Errorf("IR schemaVersion is required")
	}
	if *header.SchemaVersion != SchemaVersion {
		return fmt.Errorf("IR schemaVersion = %d, want %d", *header.SchemaVersion, SchemaVersion)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded pipelineJSON
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode IR: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	*p = Pipeline(decoded)
	return nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing IR data: %w", err)
	}
	return fmt.Errorf("IR contains multiple JSON values")
}
