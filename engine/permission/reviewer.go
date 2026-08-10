package permission

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	PermissionReviewSchemaVersion        uint16 = 1
	PermissionReviewDataBoundary                = "permission_review_v1:user_authority+host_policy+redacted_action"
	MaxPermissionReviewRationaleBytes           = 1024
	maxPermissionReviewToolCallIDBytes          = 256
	maxPermissionReviewToolTokenBytes           = 128
	maxPermissionReviewRouteValueBytes          = 256
	maxPermissionReviewRedactedArgsBytes        = 32 * 1024
	maxPermissionReviewRootFacts                = 64
	maxPermissionReviewRiskFacts                = 64
	maxPermissionReviewIntentRecords            = 3
	maxPermissionReviewIntentRecordBytes        = 2048
	maxPermissionReviewIntentTotalBytes         = 4096
	maxPermissionReviewProjectionDepth          = 8
	maxPermissionReviewProjectionItems          = 128
)

const (
	ReviewDecisionApprove  = "approve"
	ReviewDecisionDeny     = "deny"
	ReviewDecisionEscalate = "escalate"

	ReviewReasonExpectedSafe        = "expected_safe"
	ReviewReasonUnexpectedRisk      = "unexpected_risk"
	ReviewReasonInsufficientContext = "insufficient_context"
	ReviewReasonAmbiguousAction     = "ambiguous_action"
)

// ApprovalReviewer evaluates one immutable model-safe request. Its result is
// advisory evidence only; QueryEngine remains the sole permission authority.
type ApprovalReviewer interface {
	Review(context.Context, PermissionReviewRequest) (PermissionReviewResult, error)
}

// ApprovalReviewerRoute is safe diagnostic metadata for one explicitly
// configured reviewer client. It never contains endpoint or credential data.
type ApprovalReviewerRoute struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	DataBoundary string `json:"data_boundary"`
}

type PermissionReviewRequest struct {
	SchemaVersion uint16                     `json:"schema_version"`
	RequestID     string                     `json:"request_id"`
	ToolCallID    string                     `json:"tool_call_id"`
	BindingNonce  string                     `json:"binding_nonce"`
	Projection    PermissionReviewProjection `json:"projection"`
}

type PermissionReviewProjection struct {
	CanonicalTool string          `json:"canonical_tool"`
	ActionKind    string          `json:"action_kind"`
	RedactedArgs  json.RawMessage `json:"redacted_args"`
	RootFacts     []RootFact      `json:"root_facts"`
	RiskFacts     []RiskFact      `json:"risk_facts"`
	TrustedIntent []IntentRecord  `json:"trusted_intent"`
}

type RootFact struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Boundary string `json:"boundary"`
}

type RiskFact struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type IntentRecord struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

type PermissionReviewResult struct {
	SchemaVersion uint16 `json:"schema_version"`
	RequestID     string `json:"request_id"`
	ToolCallID    string `json:"tool_call_id"`
	BindingNonce  string `json:"binding_nonce"`
	Decision      string `json:"decision"`
	ReasonCode    string `json:"reason_code"`
	Rationale     string `json:"rationale"`
}

var reviewerReasons = map[string]map[string]struct{}{
	ReviewDecisionApprove: {
		ReviewReasonExpectedSafe: {},
	},
	ReviewDecisionDeny: {
		ReviewReasonUnexpectedRisk: {},
	},
	ReviewDecisionEscalate: {
		ReviewReasonInsufficientContext: {},
		ReviewReasonAmbiguousAction:     {},
	},
}

func ValidatePermissionReviewRequest(request PermissionReviewRequest) error {
	if request.SchemaVersion != PermissionReviewSchemaVersion {
		return fmt.Errorf("unsupported permission review schema version")
	}
	if !validReviewOpaqueID(request.RequestID, 16) ||
		!validReviewOpaqueID(request.BindingNonce, 32) ||
		!validReviewIdentifier(
			request.ToolCallID,
			maxPermissionReviewToolCallIDBytes,
		) {
		return fmt.Errorf("invalid permission review identity")
	}
	if !validReviewToken(
		request.Projection.CanonicalTool,
		maxPermissionReviewToolTokenBytes,
	) ||
		!validReviewToken(
			request.Projection.ActionKind,
			maxPermissionReviewToolTokenBytes,
		) ||
		len(request.Projection.RedactedArgs) == 0 ||
		len(request.Projection.RedactedArgs) > maxPermissionReviewRedactedArgsBytes ||
		!json.Valid(request.Projection.RedactedArgs) {
		return fmt.Errorf("invalid permission review projection")
	}
	if err := validatePermissionReviewRedactedArgs(
		request.Projection.RedactedArgs,
	); err != nil {
		return err
	}
	if err := validatePermissionReviewFacts(request.Projection); err != nil {
		return err
	}
	return nil
}

// ValidatePermissionReviewResult accepts only the exact v1 advisory response
// for request. Echo fields correlate the response; they do not grant authority.
func ValidatePermissionReviewResult(
	request PermissionReviewRequest,
	result PermissionReviewResult,
) error {
	if err := ValidatePermissionReviewRequest(request); err != nil {
		return err
	}
	if result.SchemaVersion != PermissionReviewSchemaVersion ||
		result.RequestID != request.RequestID ||
		result.ToolCallID != request.ToolCallID ||
		result.BindingNonce != request.BindingNonce {
		return fmt.Errorf("permission review binding mismatch")
	}
	reasons, ok := reviewerReasons[result.Decision]
	if !ok {
		return fmt.Errorf("invalid permission review decision")
	}
	if _, ok := reasons[result.ReasonCode]; !ok {
		return fmt.Errorf("invalid permission review reason")
	}
	if !utf8.ValidString(result.Rationale) ||
		strings.TrimSpace(result.Rationale) == "" ||
		len(result.Rationale) > MaxPermissionReviewRationaleBytes ||
		hasUnsafeReviewControl(result.Rationale) {
		return fmt.Errorf("invalid permission review rationale")
	}
	return nil
}

func IsSafeReviewRoute(route ApprovalReviewerRoute) bool {
	return validReviewToken(route.Provider, maxPermissionReviewToolTokenBytes) &&
		validReviewRouteValue(route.Model) &&
		route.DataBoundary == PermissionReviewDataBoundary
}

func validReviewOpaqueID(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes
}

func validatePermissionReviewFacts(
	projection PermissionReviewProjection,
) error {
	if len(projection.RootFacts) > maxPermissionReviewRootFacts ||
		len(projection.RiskFacts) > maxPermissionReviewRiskFacts ||
		len(projection.TrustedIntent) > maxPermissionReviewIntentRecords {
		return fmt.Errorf("permission review projection has too many facts")
	}
	for _, fact := range projection.RootFacts {
		if !validReviewToken(fact.Kind, maxPermissionReviewToolTokenBytes) ||
			!validReviewStructuredLabel(fact.Label) ||
			!validReviewToken(fact.Boundary, maxPermissionReviewToolTokenBytes) {
			return fmt.Errorf("invalid permission review root fact")
		}
	}
	for _, fact := range projection.RiskFacts {
		if !validReviewToken(fact.Name, maxPermissionReviewToolTokenBytes) ||
			(fact.Value != "true" && fact.Value != "false") {
			return fmt.Errorf("invalid permission review risk fact")
		}
	}
	totalIntentBytes := 0
	for _, intent := range projection.TrustedIntent {
		totalIntentBytes += len(intent.Content)
		if intent.Kind != "direct_user" ||
			!utf8.ValidString(intent.Content) ||
			strings.TrimSpace(intent.Content) == "" ||
			len(intent.Content) > maxPermissionReviewIntentRecordBytes ||
			totalIntentBytes > maxPermissionReviewIntentTotalBytes ||
			hasUnsafeReviewControl(intent.Content) {
			return fmt.Errorf("invalid permission review trusted intent")
		}
	}
	return nil
}

func validReviewToken(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validReviewRouteValue(value string) bool {
	if value == "" ||
		len(value) > maxPermissionReviewRouteValueBytes ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '-' || character == '.' ||
			character == '/' || character == ':' || character == '@' ||
			character == '+' {
			continue
		}
		return false
	}
	return true
}

func validatePermissionReviewRedactedArgs(raw json.RawMessage) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid permission review redacted arguments")
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("permission review redacted arguments are not an object")
	}
	items := 0
	if err := validatePermissionReviewProjectedValue(value, 0, &items); err != nil {
		return err
	}
	return nil
}

func validatePermissionReviewProjectedValue(
	value any,
	depth int,
	items *int,
) error {
	if depth > maxPermissionReviewProjectionDepth {
		return fmt.Errorf("permission review redacted arguments are too deep")
	}
	(*items)++
	if *items > maxPermissionReviewProjectionItems {
		return fmt.Errorf("permission review redacted arguments have too many items")
	}
	switch typed := value.(type) {
	case map[string]any:
		if kind, leaf := typed["kind"].(string); leaf {
			return validatePermissionReviewProjectedLeaf(kind, typed)
		}
		for key, child := range typed {
			if !validReviewToken(key, 64) {
				return fmt.Errorf("invalid permission review projected field")
			}
			if err := validatePermissionReviewProjectedValue(
				child,
				depth+1,
				items,
			); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validatePermissionReviewProjectedValue(
				child,
				depth+1,
				items,
			); err != nil {
				return err
			}
		}
	case bool, json.Number, nil:
		return nil
	default:
		return fmt.Errorf("invalid permission review projected value")
	}
	return nil
}

func validatePermissionReviewProjectedLeaf(
	kind string,
	leaf map[string]any,
) error {
	switch kind {
	case "redacted_secret":
		if len(leaf) != 1 {
			return fmt.Errorf("invalid permission review secret projection")
		}
	case "text":
		bytes, ok := leaf["bytes"].(json.Number)
		if len(leaf) != 2 || !ok {
			return fmt.Errorf("invalid permission review text projection")
		}
		count, err := bytes.Int64()
		if err != nil || count < 0 {
			return fmt.Errorf("invalid permission review text size")
		}
	case "path":
		label, labelOK := leaf["label"].(string)
		boundary, boundaryOK := leaf["boundary"].(string)
		if len(leaf) != 3 ||
			!labelOK ||
			!boundaryOK ||
			!validReviewStructuredLabel(label) ||
			!validReviewToken(boundary, maxPermissionReviewToolTokenBytes) {
			return fmt.Errorf("invalid permission review path projection")
		}
	default:
		return fmt.Errorf("invalid permission review projection kind")
	}
	return nil
}

func validReviewIdentifier(value string, limit int) bool {
	return value != "" &&
		len(value) <= limit &&
		strings.TrimSpace(value) == value &&
		utf8.ValidString(value) &&
		!hasUnsafeReviewControl(value)
}

func validReviewStructuredLabel(value string) bool {
	if value == "" || len(value) > maxPermissionReviewRouteValueBytes {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if !validReviewToken(part, maxPermissionReviewToolTokenBytes) {
			return false
		}
	}
	return true
}

func hasUnsafeReviewControl(value string) bool {
	for _, character := range value {
		if character < 0x20 && character != '\n' &&
			character != '\r' && character != '\t' {
			return true
		}
		if character == 0x7f {
			return true
		}
	}
	return false
}
