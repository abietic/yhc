package agenticdeepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxFileUploadBytes   int64 = 64 << 20
	maxFileNameRunes           = 512
	minFileExpirySeconds       = 3600
	maxFileExpirySeconds       = 2592000
	maxFilesListLimit          = 1000
	maxFileIDBytes             = 512
)

// FilePurpose is the provider-owned purpose assigned to an uploaded file.
type FilePurpose string

const (
	// FilePurposeUserData is the only purpose accepted by DeepSeek Files API.
	FilePurposeUserData FilePurpose = "user_data"
)

// FileOrder controls Files API creation-time ordering.
type FileOrder string

const (
	FileOrderAsc  FileOrder = "asc"
	FileOrderDesc FileOrder = "desc"
)

// FilesConfig configures one immutable DeepSeek Files API client.
type FilesConfig struct {
	APIKey string
	// BaseURL is an API root such as https://api.deepseek.com. The client
	// appends /files while preserving an existing proxy path prefix.
	BaseURL string

	// HTTPClient takes precedence over Timeout when supplied.
	HTTPClient *http.Client
	Timeout    time.Duration
}

// UploadFileParams describes one bounded image upload. Size must be the exact
// number of bytes exposed by Content so the provider's 64 MiB limit can be
// enforced before network dispatch.
type UploadFileParams struct {
	Filename string
	Content  io.Reader
	Size     int64

	// ExpiresAfterSeconds is optional. Non-zero values are anchored at file
	// creation and must be between one hour and 30 days.
	ExpiresAfterSeconds int
}

// ListFilesOptions controls the optional Files API filters and cursor.
type ListFilesOptions struct {
	After   string
	Limit   int
	Order   FileOrder
	Purpose FilePurpose
}

// FileObject is a DeepSeek Files API image resource.
type FileObject struct {
	ID        string      `json:"id"`
	Object    string      `json:"object"`
	Bytes     int64       `json:"bytes"`
	CreatedAt int64       `json:"created_at"`
	Filename  string      `json:"filename"`
	Purpose   FilePurpose `json:"purpose"`
	ExpiresAt *int64      `json:"expires_at,omitempty"`
}

// FileList is one cursor-paginated Files API result.
type FileList struct {
	Object  string       `json:"object"`
	Data    []FileObject `json:"data"`
	FirstID string       `json:"first_id"`
	LastID  string       `json:"last_id"`
	HasMore bool         `json:"has_more"`
}

// DeletedFile is the Files API deletion receipt.
type DeletedFile struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// FilesValidationError identifies a local Files API request rejection.
type FilesValidationError struct {
	ReasonCode string
}

func (e *FilesValidationError) Error() string {
	return "agenticdeepseek: Files API request validation failed: " + e.ReasonCode
}

// FilesClient owns DeepSeek's OpenAI-shaped Files API without routing model
// generation through an OpenAI compatibility SDK.
type FilesClient struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
}

// NewFilesClient creates a dedicated DeepSeek Files API client. It performs
// only local validation and never contacts the provider.
func NewFilesClient(config *FilesConfig) (*FilesClient, error) {
	if config == nil {
		return nil, filesValidationError("config_nil")
	}
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, filesValidationError("api_key_missing")
	}
	endpoint, err := filesEndpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.Timeout < 0 {
		return nil, filesValidationError("timeout_invalid")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: config.Timeout}
	}
	return &FilesClient{httpClient: httpClient, endpoint: endpoint, apiKey: apiKey}, nil
}

// Upload creates one Files API image resource using multipart/form-data.
func (c *FilesClient) Upload(ctx context.Context, params UploadFileParams) (*FileObject, error) {
	if err := validateUploadFileParams(params); err != nil {
		return nil, err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", string(FilePurposeUserData)); err != nil {
		return nil, &ProtocolError{ReasonCode: "files_multipart_build_failed"}
	}
	if params.ExpiresAfterSeconds != 0 {
		if err := writer.WriteField("expires_after[anchor]", "created_at"); err != nil {
			return nil, &ProtocolError{ReasonCode: "files_multipart_build_failed"}
		}
		if err := writer.WriteField("expires_after[seconds]", strconv.Itoa(params.ExpiresAfterSeconds)); err != nil {
			return nil, &ProtocolError{ReasonCode: "files_multipart_build_failed"}
		}
	}
	part, err := writer.CreateFormFile("file", params.Filename)
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "files_multipart_build_failed"}
	}
	written, err := io.CopyN(part, params.Content, params.Size)
	if err != nil || written != params.Size {
		return nil, filesValidationError("file_content_short")
	}
	extra, extraErr := io.CopyN(io.Discard, params.Content, 1)
	if extra > 0 {
		return nil, filesValidationError("file_content_long")
	}
	if extraErr != nil && !errors.Is(extraErr, io.EOF) {
		return nil, filesValidationError("file_content_read_failed")
	}
	if err := writer.Close(); err != nil {
		return nil, &ProtocolError{ReasonCode: "files_multipart_build_failed"}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "files_request_build_failed"}
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	var object FileObject
	if err := c.doJSON(req, &object); err != nil {
		return nil, err
	}
	if err := validateFileObject(object); err != nil {
		return nil, err
	}
	return &object, nil
}

// List returns one cursor-paginated set of uploaded files.
func (c *FilesClient) List(ctx context.Context, options *ListFilesOptions) (*FileList, error) {
	query, err := listFilesQuery(options)
	if err != nil {
		return nil, err
	}
	endpoint := c.endpoint
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "files_request_build_failed"}
	}
	var list FileList
	if err := c.doJSON(req, &list); err != nil {
		return nil, err
	}
	if list.Object != "list" {
		return nil, &ProtocolError{ReasonCode: "files_list_object_invalid"}
	}
	for _, object := range list.Data {
		if err := validateFileObject(object); err != nil {
			return nil, err
		}
	}
	for _, cursor := range []string{list.FirstID, list.LastID} {
		if cursor != "" && !validFileID(cursor) {
			return nil, &ProtocolError{ReasonCode: "files_list_cursor_invalid"}
		}
	}
	return &list, nil
}

// Retrieve returns metadata for one uploaded file.
func (c *FilesClient) Retrieve(ctx context.Context, fileID string) (*FileObject, error) {
	endpoint, err := c.resourceEndpoint(fileID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "files_request_build_failed"}
	}
	var object FileObject
	if err := c.doJSON(req, &object); err != nil {
		return nil, err
	}
	if err := validateFileObject(object); err != nil {
		return nil, err
	}
	return &object, nil
}

// Delete removes one uploaded file and returns the provider receipt.
func (c *FilesClient) Delete(ctx context.Context, fileID string) (*DeletedFile, error) {
	endpoint, err := c.resourceEndpoint(fileID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "files_request_build_failed"}
	}
	var deleted DeletedFile
	if err := c.doJSON(req, &deleted); err != nil {
		return nil, err
	}
	if !validFileID(deleted.ID) || deleted.Object != "file" || !deleted.Deleted {
		return nil, &ProtocolError{ReasonCode: "files_delete_response_invalid"}
	}
	return &deleted, nil
}

func (c *FilesClient) resourceEndpoint(fileID string) (string, error) {
	fileID = strings.TrimSpace(fileID)
	if !validFileID(fileID) {
		return "", filesValidationError("file_id_invalid")
	}
	return c.endpoint + "/" + url.PathEscape(fileID), nil
}

func (c *FilesClient) doJSON(req *http.Request, target any) error {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return &transportError{err: ctxErr}
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err
		}
		return &transportError{err: err}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return c.decodeAPIError(response)
	}
	raw, err := readBounded(response.Body, maxResponseBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return &ProtocolError{ReasonCode: "files_response_json_invalid"}
	}
	return nil
}

func (c *FilesClient) decodeAPIError(response *http.Response) error {
	raw, readErr := readBounded(response.Body, maxErrorBytes)
	if readErr != nil {
		return &APIError{StatusCode: response.StatusCode, RequestID: requestID(response.Header)}
	}
	var envelope struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	code := rawScalar(envelope.Error.Code)
	if code == "" {
		code = envelope.Error.Type
	}
	message := redactExact(envelope.Error.Message, c.apiKey, c.endpoint)
	return &APIError{
		StatusCode: response.StatusCode,
		Code:       boundedText(code, 128),
		Message:    boundedText(message, 1024),
		RequestID:  requestID(response.Header),
	}
}

func filesEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", filesValidationError("base_url_invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", filesValidationError("base_url_invalid")
	}
	cleanedPath := strings.TrimSuffix(parsed.Path, "/")
	if !strings.HasSuffix(cleanedPath, "/files") {
		cleanedPath = path.Join(cleanedPath, "files")
	}
	if !strings.HasPrefix(cleanedPath, "/") {
		cleanedPath = "/" + cleanedPath
	}
	parsed.Path = cleanedPath
	parsed.RawPath = ""
	return parsed.String(), nil
}

func validateUploadFileParams(params UploadFileParams) error {
	filename := strings.TrimSpace(params.Filename)
	if filename == "" || filename != params.Filename || strings.ContainsAny(filename, "/\\\x00") {
		return filesValidationError("filename_invalid")
	}
	if !utf8.ValidString(filename) || utf8.RuneCountInString(filename) > maxFileNameRunes {
		return filesValidationError("filename_too_long")
	}
	if params.Content == nil {
		return filesValidationError("file_content_nil")
	}
	if params.Size <= 0 {
		return filesValidationError("file_size_invalid")
	}
	if params.Size > maxFileUploadBytes {
		return filesValidationError("file_size_exceeded")
	}
	if params.ExpiresAfterSeconds != 0 &&
		(params.ExpiresAfterSeconds < minFileExpirySeconds || params.ExpiresAfterSeconds > maxFileExpirySeconds) {
		return filesValidationError("file_expiry_invalid")
	}
	return nil
}

func listFilesQuery(options *ListFilesOptions) (url.Values, error) {
	query := make(url.Values)
	if options == nil {
		return query, nil
	}
	if options.After != "" {
		if !validFileID(options.After) {
			return nil, filesValidationError("after_file_id_invalid")
		}
		query.Set("after", options.After)
	}
	if options.Limit < 0 || options.Limit > maxFilesListLimit {
		return nil, filesValidationError("list_limit_invalid")
	}
	if options.Limit > 0 {
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	switch options.Order {
	case "":
	case FileOrderAsc, FileOrderDesc:
		query.Set("order", string(options.Order))
	default:
		return nil, filesValidationError("list_order_invalid")
	}
	switch options.Purpose {
	case "":
	case FilePurposeUserData:
		query.Set("purpose", string(options.Purpose))
	default:
		return nil, filesValidationError("file_purpose_invalid")
	}
	return query, nil
}

func validateFileObject(object FileObject) error {
	if !validFileID(object.ID) || object.Object != "file" || object.Bytes < 0 ||
		object.CreatedAt <= 0 || strings.TrimSpace(object.Filename) == "" || object.Purpose != FilePurposeUserData {
		return &ProtocolError{ReasonCode: "files_object_invalid"}
	}
	return nil
}

func validFileID(fileID string) bool {
	if !strings.HasPrefix(fileID, "file-api-") || len(fileID) == len("file-api-") || len(fileID) > maxFileIDBytes {
		return false
	}
	for _, char := range fileID[len("file-api-"):] {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func filesValidationError(reason string) error {
	return &FilesValidationError{ReasonCode: reason}
}
