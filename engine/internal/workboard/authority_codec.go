package workboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func EncodeAuthorityRecord(record AuthorityRecord) ([]byte, error) {
	if err := validateAuthorityRecord(record, record.SessionID); err != nil {
		return nil, err
	}
	return encodeAuthorityJSON("record", record)
}

func DecodeAuthorityRecord(
	data []byte,
	expectedSessionID string,
) (AuthorityRecord, error) {
	var record AuthorityRecord
	if err := decodeAuthorityJSON("record", data, &record); err != nil {
		return AuthorityRecord{}, err
	}
	if err := validateAuthorityRecord(record, expectedSessionID); err != nil {
		return AuthorityRecord{}, err
	}
	return cloneAuthorityRecord(record), nil
}

func EncodeAuthorityMarker(marker AuthorityMarker) ([]byte, error) {
	if err := validateAuthorityMarker(marker, marker.SessionID); err != nil {
		return nil, err
	}
	return encodeAuthorityJSON("marker", marker)
}

func DecodeAuthorityMarker(
	data []byte,
	expectedSessionID string,
) (AuthorityMarker, error) {
	var marker AuthorityMarker
	if err := decodeAuthorityJSON("marker", data, &marker); err != nil {
		return AuthorityMarker{}, err
	}
	if err := validateAuthorityMarker(marker, expectedSessionID); err != nil {
		return AuthorityMarker{}, err
	}
	return marker, nil
}

func EncodeLegacyBackup(backup LegacyBackup) ([]byte, error) {
	if err := validateLegacyBackup(backup, backup.SessionID); err != nil {
		return nil, err
	}
	return encodeAuthorityJSON("backup", backup)
}

func DecodeLegacyBackup(
	data []byte,
	expectedSessionID string,
) (LegacyBackup, error) {
	var backup LegacyBackup
	if err := decodeAuthorityJSON("backup", data, &backup); err != nil {
		return LegacyBackup{}, err
	}
	if err := validateLegacyBackup(backup, expectedSessionID); err != nil {
		return LegacyBackup{}, err
	}
	return cloneLegacyBackup(backup), nil
}

func encodeAuthorityJSON(kind string, value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf(
			"workboard authority: encode %s: %w",
			kind,
			err,
		)
	}
	if len(data)+1 > MaxEncodedJSONBytes {
		return nil, fmt.Errorf(
			"workboard authority: encoded %s exceeds %d bytes",
			kind,
			MaxEncodedJSONBytes,
		)
	}
	return append(data, '\n'), nil
}

func decodeAuthorityJSON(kind string, data []byte, target any) error {
	if len(data) > MaxEncodedJSONBytes {
		return fmt.Errorf(
			"workboard authority: encoded %s exceeds %d bytes",
			kind,
			MaxEncodedJSONBytes,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf(
			"workboard authority: decode %s: %w",
			kind,
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf(
				"workboard authority: trailing %s JSON value",
				kind,
			)
		}
		return fmt.Errorf(
			"workboard authority: trailing %s data: %w",
			kind,
			err,
		)
	}
	return nil
}
