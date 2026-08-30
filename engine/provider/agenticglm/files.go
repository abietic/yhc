package agenticglm

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
	maxAgentFileUploadBytes int64 = 20 << 20
	maxFileNameRunes              = 512
	maxFileIDBytes                = 512
	maxFilesListLimit             = 100
)

// FilePurpose is the provider-owned purpose assigned to an uploaded file.
type FilePurpose string

const (
	// FilePurposeAgent is the exact purpose implemented for reusable
	// GLM-5.3-Flash model input.
	FilePurposeAgent FilePurpose = "agent"
)

// FileOrder is the only ordering key currently documented by GLM Files API.
type FileOrder string

const FileOrderCreatedAt FileOrder = "created_at"

// FilesConfig configures an immutable GLM Files API client.
type FilesConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// UploadFileParams describes one bounded upload. PurposeAgent is the purpose
// intended for model/agent input and has a documented 20 MiB per-file limit.
type UploadFileParams struct {
	Filename string
	Content  io.Reader
	Size     int64
	Purpose  FilePurpose
}

// ListFilesOptions controls the official cursor, purpose, ordering, and limit.
type ListFilesOptions struct {
	After   string
	Purpose FilePurpose
	Order   FileOrder
	Limit   int
}

// FileObject is one GLM Files API resource.
type FileObject struct {
	ID        string      `json:"id"`
	Object    string      `json:"object"`
	Bytes     int64       `json:"bytes"`
	CreatedAt int64       `json:"created_at"`
	Filename  string      `json:"filename"`
	Purpose   FilePurpose `json:"purpose"`
}

// FileList is one cursor-paginated Files API response.
type FileList struct {
	Object  string       `json:"object"`
	Data    []FileObject `json:"data"`
	HasMore bool         `json:"has_more"`
}

// DeletedFile is the provider deletion receipt.
type DeletedFile struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

// FilesValidationError is a local rejection that never formats file content.
type FilesValidationError struct{ ReasonCode string }

func (e *FilesValidationError) Error() string {
	return "agenticglm: Files API request validation failed: " + e.ReasonCode
}

// FilesClient owns GLM's Files API and shares no OpenAI compatibility client.
type FilesClient struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
}

// NewFilesClient performs local construction only.
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

// Upload creates one file resource through multipart/form-data.
func (c *FilesClient) Upload(ctx context.Context, params UploadFileParams) (*FileObject, error) {
	if err := validateUploadFileParams(params); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", string(params.Purpose)); err != nil {
		return nil, &ProtocolError{ReasonCode: "files_multipart_build_failed"}
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

// List returns one page of uploaded files.
func (c *FilesClient) List(ctx context.Context, options *ListFilesOptions) (*FileList, error) {
	query, err := listFilesQuery(options)
	if err != nil {
		return nil, err
	}
	endpoint := c.endpoint + "?" + query.Encode()
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
	return &list, nil
}

// Delete permanently removes one uploaded file.
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
	response, err := c.do(req)
	if err != nil {
		return err
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

func (c *FilesClient) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, &transportError{err: ctxErr}
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err
		}
		return nil, &transportError{err: err}
	}
	return response, nil
}

func (c *FilesClient) decodeAPIError(response *http.Response) error {
	raw, readErr := readBounded(response.Body, maxErrorBytes)
	if readErr != nil {
		return &APIError{StatusCode: response.StatusCode, RequestID: responseRequestID(response.Header)}
	}
	var envelope struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(raw, &envelope)
	code := rawScalar(envelope.Error.Code)
	if code == "" {
		code = rawScalar(envelope.Code)
	}
	message := envelope.Error.Message
	if message == "" {
		message = envelope.Message
	}
	message = redactExact(message, c.apiKey, c.endpoint)
	return &APIError{
		StatusCode: response.StatusCode,
		Code:       boundedText(code, 128),
		Message:    boundedText(message, 1024),
		RequestID:  responseRequestID(response.Header),
	}
}

func filesEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
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
	if filename == "" || filename != params.Filename || strings.ContainsAny(filename, "/\\\x00") ||
		!utf8.ValidString(filename) || utf8.RuneCountInString(filename) > maxFileNameRunes {
		return filesValidationError("filename_invalid")
	}
	if params.Content == nil {
		return filesValidationError("file_content_missing")
	}
	if params.Size < 1 || params.Size > maxAgentFileUploadBytes {
		return filesValidationError("file_size_invalid")
	}
	if !validFilePurpose(params.Purpose) {
		return filesValidationError("purpose_invalid")
	}
	return nil
}

func listFilesQuery(options *ListFilesOptions) (url.Values, error) {
	if options == nil || !validFilePurpose(options.Purpose) {
		return nil, filesValidationError("purpose_invalid")
	}
	query := url.Values{"purpose": []string{string(options.Purpose)}}
	if options.After != "" {
		if !validFileID(options.After) {
			return nil, filesValidationError("after_invalid")
		}
		query.Set("after", options.After)
	}
	if options.Limit != 0 {
		if options.Limit < 1 || options.Limit > maxFilesListLimit {
			return nil, filesValidationError("limit_invalid")
		}
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.Order != "" {
		if options.Order != FileOrderCreatedAt {
			return nil, filesValidationError("order_invalid")
		}
		query.Set("order", string(options.Order))
	}
	return query, nil
}

func validFilePurpose(purpose FilePurpose) bool {
	return purpose == FilePurposeAgent
}

func validFileID(fileID string) bool {
	if fileID == "" || len(fileID) > maxFileIDBytes || !utf8.ValidString(fileID) {
		return false
	}
	for _, r := range fileID {
		if r <= 0x20 || r == 0x7f || r == '/' || r == '\\' || r == '?' || r == '#' {
			return false
		}
	}
	return true
}

func validateFileObject(object FileObject) error {
	if !validFileID(object.ID) || object.Object != "file" || object.Bytes < 0 || object.CreatedAt <= 0 ||
		strings.TrimSpace(object.Filename) == "" || !validFilePurpose(object.Purpose) {
		return &ProtocolError{ReasonCode: "files_object_invalid"}
	}
	return nil
}

func filesValidationError(reason string) *FilesValidationError {
	return &FilesValidationError{ReasonCode: reason}
}
