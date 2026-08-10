package workboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func Encode(record Record) ([]byte, error) {
	if err := validateRecord(record, record.SessionID); err != nil {
		return nil, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("workboard: encode record: %w", err)
	}
	if len(data)+1 > MaxEncodedJSONBytes {
		return nil, fmt.Errorf(
			"workboard: encoded record exceeds %d bytes",
			MaxEncodedJSONBytes,
		)
	}
	return append(data, '\n'), nil
}

func Decode(data []byte, expectedSessionID string) (Record, error) {
	record, err := decodeRecord(data)
	if err != nil {
		return Record{}, err
	}
	if err := validateRecord(record, expectedSessionID); err != nil {
		return Record{}, err
	}
	return record.clone(), nil
}

func decodeRecord(data []byte) (Record, error) {
	if len(data) > MaxEncodedJSONBytes {
		return Record{}, fmt.Errorf(
			"workboard: encoded record exceeds %d bytes",
			MaxEncodedJSONBytes,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("workboard: decode record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Record{}, fmt.Errorf("workboard: trailing JSON value")
		}
		return Record{}, fmt.Errorf("workboard: trailing data: %w", err)
	}
	return record, nil
}
