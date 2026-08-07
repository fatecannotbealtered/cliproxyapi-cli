// Package output implements the CLI's machine-readable stdout contract.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/contract"
)

const (
	FormatJSON = "json"
	FormatText = "text"
	FormatRaw  = "raw"
)

// Options controls one command invocation's output.
type Options struct {
	Format    string
	Compact   bool
	Fields    []string
	StartedAt time.Time
}

// Printer writes exactly one command result to stdout.
type Printer struct {
	out     io.Writer
	options Options
}

func NewPrinter(out io.Writer, options Options) *Printer {
	if options.Format == "" {
		options.Format = FormatJSON
	}
	return &Printer{out: out, options: options}
}

type meta struct {
	DurationMS int64 `json:"duration_ms"`
}

type successEnvelope struct {
	OK            bool   `json:"ok"`
	SchemaVersion string `json:"schema_version"`
	Data          any    `json:"data"`
	Meta          meta   `json:"meta"`
}

type errorObject struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	Retryable bool           `json:"retryable"`
}

type errorEnvelope struct {
	OK            bool        `json:"ok"`
	SchemaVersion string      `json:"schema_version"`
	Error         errorObject `json:"error"`
	Meta          meta        `json:"meta"`
}

// CLIError is a stable E_* error with its canonical exit and retry semantics.
type CLIError struct {
	Code    string
	Message string
	Details map[string]any
	Cause   error
}

func NewError(code, message string, details map[string]any) *CLIError {
	if _, ok := contract.Codes[code]; !ok {
		code = "E_UNKNOWN"
	}
	if details == nil {
		details = map[string]any{}
	}
	return &CLIError{Code: code, Message: message, Details: details}
}

func WrapError(code, message string, cause error, details map[string]any) *CLIError {
	err := NewError(code, message, details)
	err.Cause = cause
	return err
}

func (e *CLIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *CLIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CLIError) ExitCode() int {
	if e == nil {
		return 0
	}
	return contract.ExitFor(e.Code)
}

func (p *Printer) commandMeta() meta {
	duration := int64(0)
	if !p.options.StartedAt.IsZero() {
		duration = time.Since(p.options.StartedAt).Milliseconds()
	}
	return meta{DurationMS: duration}
}

func (p *Printer) Success(data any) error {
	if p.options.Format == FormatRaw {
		return errors.New("raw output requires Raw()")
	}
	projected, err := projectFields(data, p.options.Fields)
	if err != nil {
		return err
	}
	if p.options.Format == FormatText {
		return p.writeJSON(projected)
	}
	return p.writeJSON(successEnvelope{
		OK:            true,
		SchemaVersion: contract.SchemaVersion,
		Data:          projected,
		Meta:          p.commandMeta(),
	})
}

func (p *Printer) Failure(cliErr *CLIError) error {
	if cliErr == nil {
		cliErr = NewError("E_UNKNOWN", "unknown error", nil)
	}
	if p.options.Format == FormatText {
		_, err := fmt.Fprintf(p.out, "%s: %s\n", cliErr.Code, cliErr.Message)
		return err
	}
	return p.writeJSON(errorEnvelope{
		OK:            false,
		SchemaVersion: contract.SchemaVersion,
		Error: errorObject{
			Code:      cliErr.Code,
			Message:   cliErr.Message,
			Details:   cliErr.Details,
			Retryable: contract.Retryable(cliErr.Code),
		},
		Meta: p.commandMeta(),
	})
}

func (p *Printer) Raw(data []byte) error {
	if p.options.Format != FormatRaw {
		return errors.New("Raw() requires --format raw")
	}
	_, err := p.out.Write(data)
	return err
}

func (p *Printer) writeJSON(value any) error {
	var (
		encoded []byte
		err     error
	)
	if p.options.Compact {
		encoded, err = json.Marshal(value)
	} else {
		encoded, err = json.MarshalIndent(value, "", "  ")
	}
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = p.out.Write(encoded)
	return err
}

func projectFields(data any, fields []string) (any, error) {
	if len(fields) == 0 {
		return data, nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("--fields requires an object result: %w", err)
	}
	projected := make(map[string]any, len(fields))
	for _, rawField := range fields {
		field := strings.TrimSpace(rawField)
		if field == "" {
			continue
		}
		value, ok := object[field]
		if !ok {
			return nil, fmt.Errorf("unknown output field %q", field)
		}
		projected[field] = value
	}
	return projected, nil
}
