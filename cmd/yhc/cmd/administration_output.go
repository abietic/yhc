package cmd

import (
	"encoding/json"
	"fmt"
	"io"
)

const administrationEnvelopeSchemaVersion = 1

type administrationEnvelope struct {
	SchemaVersion int                          `json:"schema_version"`
	Operation     string                       `json:"operation"`
	Status        string                       `json:"status"`
	ExitCode      int                          `json:"exit_code"`
	Result        any                          `json:"result,omitempty"`
	Error         *administrationEnvelopeError `json:"error,omitempty"`
}

type administrationEnvelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type administrationOutput struct {
	text     string
	result   any
	warnings []string
}

func renderAdministrationSuccess(
	format outputFormat,
	stdout io.Writer,
	stderr io.Writer,
	operation string,
	output administrationOutput,
	outputLabel string,
) error {
	if format == outputFormatJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(administrationEnvelope{
			SchemaVersion: administrationEnvelopeSchemaVersion,
			Operation:     operation,
			Status:        "completed",
			ExitCode:      ExitSuccess,
			Result:        output.result,
		})
	}
	if output.text != "" {
		if _, err := fmt.Fprintln(stdout, output.text); err != nil {
			return fmt.Errorf("write %s output: %w", outputLabel, err)
		}
	}
	for _, warning := range output.warnings {
		if _, err := fmt.Fprintf(stderr, "Warning: %s\n", redactSensitiveText(warning)); err != nil {
			return fmt.Errorf("write %s warning: %w", outputLabel, err)
		}
	}
	return nil
}

func renderAdministrationFailure(
	format outputFormat,
	stdout io.Writer,
	stderr io.Writer,
	operation string,
	err error,
	code string,
	exitCode int,
	outputLabel string,
) error {
	return renderAdministrationFailureWithResult(
		format,
		stdout,
		stderr,
		operation,
		err,
		code,
		exitCode,
		outputLabel,
		nil,
	)
}

func renderAdministrationFailureWithResult(
	format outputFormat,
	stdout io.Writer,
	stderr io.Writer,
	operation string,
	err error,
	code string,
	exitCode int,
	outputLabel string,
	result any,
) error {
	safeErr := sanitizeHeadlessError(err)
	status := "failed"
	if exitCode == ExitCancelled {
		status = "cancelled"
	}
	if format == outputFormatJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if encodeErr := encoder.Encode(administrationEnvelope{
			SchemaVersion: administrationEnvelopeSchemaVersion,
			Operation:     operation,
			Status:        status,
			ExitCode:      exitCode,
			Result:        result,
			Error: &administrationEnvelopeError{
				Code:    code,
				Message: safeErr.Error(),
			},
		}); encodeErr != nil {
			return fmt.Errorf("write %s failure output: %w", outputLabel, encodeErr)
		}
	} else if _, writeErr := fmt.Fprintf(stderr, "Error: %s\n", safeErr); writeErr != nil {
		return fmt.Errorf("write %s failure: %w", outputLabel, writeErr)
	}
	return renderedExitError(exitCode, safeErr)
}
