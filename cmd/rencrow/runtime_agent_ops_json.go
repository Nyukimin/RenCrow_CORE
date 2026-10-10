package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var errAgentOpsInvalidJSON = errors.New("invalid agent ops JSON request")

func readAgentOpsRequestBody(body io.Reader) ([]byte, error) {
	// Keep the raw body allocation at the existing request cap, then probe one byte for overflow.
	raw := make([]byte, agentOpsMaxBodyBytes)
	count, err := io.ReadFull(body, raw)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return raw[:count], nil
	}
	if err != nil {
		return nil, err
	}

	var extra [1]byte
	count, err = io.ReadFull(body, extra[:])
	if count != 0 {
		return nil, errAgentOpsRequestTooLarge
	}
	if errors.Is(err, io.EOF) {
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, errAgentOpsRequestTooLarge
}

func decodeStrictAgentOpsRequest(raw []byte) (agentOpsRequest, error) {
	var request agentOpsRequest
	if !utf8.Valid(raw) || !validAgentOpsUnicodeEscapes(raw) {
		return request, errAgentOpsInvalidJSON
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return request, errAgentOpsInvalidJSON
	}

	var seen uint8
	var targetRaw json.RawMessage
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return request, errAgentOpsInvalidJSON
		}
		key, ok := token.(string)
		if !ok {
			return request, errAgentOpsInvalidJSON
		}
		field := agentOpsRequestField(key)
		if field == 0 || seen&field != 0 {
			return request, errAgentOpsInvalidJSON
		}
		seen |= field
		var destination *string
		switch field {
		case agentOpsMessageField:
			destination = &request.Message
		case agentOpsOperationField:
			destination = &request.Operation
		case agentOpsQueryField:
			destination = &request.Query
		case agentOpsTargetField:
			if err := decoder.Decode(&targetRaw); err != nil {
				return request, errAgentOpsInvalidJSON
			}
			continue
		}
		if err := decoder.Decode(destination); err != nil {
			return request, errAgentOpsInvalidJSON
		}
	}

	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return request, errAgentOpsInvalidJSON
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return request, errAgentOpsInvalidJSON
	}
	if targetRaw != nil {
		target, err := decodeStrictAgentOpsNativeResumeTarget(targetRaw)
		if err != nil {
			return request, errAgentOpsInvalidJSON
		}
		request.ResumeTarget = &target
	}
	return request, nil
}

const (
	agentOpsMessageField uint8 = 1 << iota
	agentOpsOperationField
	agentOpsQueryField
	agentOpsTargetField
)

func agentOpsRequestField(key string) uint8 {
	switch {
	case strings.EqualFold(key, "message"):
		return agentOpsMessageField
	case strings.EqualFold(key, "operation"):
		return agentOpsOperationField
	case strings.EqualFold(key, "query"):
		return agentOpsQueryField
	case strings.EqualFold(key, "target"):
		return agentOpsTargetField
	default:
		return 0
	}
}

type agentOpsNativeResumeTarget struct {
	TaskID            modulecore.TaskID `json:"task_id"`
	ExpectedCoreRunID modulecore.RunID  `json:"expected_core_run_id"`
}

func decodeStrictAgentOpsNativeResumeTarget(raw []byte) (agentOpsNativeResumeTarget, error) {
	var target agentOpsNativeResumeTarget
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return target, errAgentOpsInvalidJSON
	}
	var seen uint8
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return target, errAgentOpsInvalidJSON
		}
		key, ok := token.(string)
		if !ok {
			return target, errAgentOpsInvalidJSON
		}
		var field uint8
		switch {
		case strings.EqualFold(key, "task_id"):
			field = 1
		case strings.EqualFold(key, "expected_core_run_id"):
			field = 2
		default:
			return target, errAgentOpsInvalidJSON
		}
		if seen&field != 0 {
			return target, errAgentOpsInvalidJSON
		}
		seen |= field
		var value string
		if err := decoder.Decode(&value); err != nil {
			return target, errAgentOpsInvalidJSON
		}
		if field == 1 {
			target.TaskID = modulecore.TaskID(value)
		} else {
			target.ExpectedCoreRunID = modulecore.RunID(value)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || seen != 3 {
		return target, errAgentOpsInvalidJSON
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || target.TaskID.Validate() != nil || target.ExpectedCoreRunID.Validate() != nil {
		return target, errAgentOpsInvalidJSON
	}
	return target, nil
}

func validAgentOpsUnicodeEscapes(raw []byte) bool {
	// encoding/json replaces unpaired surrogate escapes with U+FFFD, so reject them before decoding.
	inString := false
	for index := 0; index < len(raw); index++ {
		if !inString {
			if raw[index] == '"' {
				inString = true
			}
			continue
		}
		switch raw[index] {
		case '"':
			inString = false
		case '\\':
			if index+1 >= len(raw) {
				return false
			}
			if raw[index+1] != 'u' {
				index++
				continue
			}
			codeUnit, ok := agentOpsHexCodeUnit(raw, index+2)
			if !ok {
				return false
			}
			index += 5
			switch {
			case codeUnit >= 0xD800 && codeUnit <= 0xDBFF:
				if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
					return false
				}
				low, ok := agentOpsHexCodeUnit(raw, index+3)
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				index += 6
			case codeUnit >= 0xDC00 && codeUnit <= 0xDFFF:
				return false
			}
		}
	}
	return !inString
}

func agentOpsHexCodeUnit(raw []byte, start int) (uint16, bool) {
	if start+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, digit := range raw[start : start+4] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
