package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cliproxyapicli "github.com/fatecannotbealtered/cliproxyapi-cli"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/api"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/config"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/credential"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/spf13/cobra"
)

// version may be overridden by release builds through -ldflags -X.
var version string

func init() {
	if version == "" {
		version = cliproxyapicli.Version
	}
}

type application struct {
	in  io.Reader
	out io.Writer
	err io.Writer

	startedAt time.Time
	format    string
	jsonAlias bool
	compact   bool
	fields    []string
	quiet     bool
	dryRun    bool
	confirm   string
	store     credential.Store

	baseURL         string
	stateDir        string
	timeout         time.Duration
	managementStdin bool

	suppressUpdateNotices bool
}

// Execute is the process entry point.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exit := ExecuteArgs(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(exit)
}

// ExecuteArgs executes one isolated command invocation and returns its process exit code.
func ExecuteArgs(ctx context.Context, args []string, in io.Reader, stdout, stderr io.Writer) int {
	return executeArgsWithCredentialStore(ctx, args, in, stdout, stderr, credential.NewOSStore())
}

func executeArgsWithCredentialStore(ctx context.Context, args []string, in io.Reader, stdout, stderr io.Writer, store credential.Store) int {
	app := &application{
		in:        in,
		out:       stdout,
		err:       stderr,
		startedAt: time.Now(),
		format:    output.FormatJSON,
		store:     store,
	}
	root := app.rootCommand()
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	cliErr := asCLIError(err)
	if command, _, findErr := root.Find(args); findErr == nil && command.Name() == "update" {
		cliErr = ensureUpdateFailureDetails(cliErr)
	}
	printer := app.printer()
	if printErr := printer.Failure(cliErr); printErr != nil {
		_, _ = fmt.Fprintf(stderr, "failed to write error output: %v\n", printErr)
		return 1
	}
	return cliErr.ExitCode()
}

func (a *application) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "cliproxyapi-cli",
		Short:         "Inspect CLIProxyAPI accounts and quota with conservative controls",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.validateOutput(cmd)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&a.format, "format", output.FormatJSON, "Output format: json, text, or raw")
	root.PersistentFlags().BoolVar(&a.jsonAlias, "json", false, "Compatibility alias for --format json")
	root.PersistentFlags().BoolVar(&a.compact, "compact", false, "Emit compact JSON")
	root.PersistentFlags().StringSliceVar(&a.fields, "fields", nil, "Return selected top-level data fields")
	root.PersistentFlags().BoolVar(&a.quiet, "quiet", false, "Suppress non-error stderr diagnostics")
	root.PersistentFlags().BoolVar(&a.dryRun, "dry-run", false, "Preview a write and issue a confirmation token")
	root.PersistentFlags().StringVar(&a.confirm, "confirm", "", "Execute the exact preview authorized by this token")
	root.PersistentFlags().StringVar(&a.baseURL, "base-url", "", "Full CLIProxyAPI Management base URL")
	root.PersistentFlags().StringVar(&a.stateDir, "state-dir", "", "Local guard and confirmation state directory")
	root.PersistentFlags().DurationVar(&a.timeout, "timeout", 0, "Management request timeout")
	root.PersistentFlags().BoolVar(&a.managementStdin, "management-key-stdin", false, "Read the management key as one line from stdin")

	root.AddCommand(a.referenceCommand(root))
	root.AddCommand(a.contextCommand())
	root.AddCommand(a.doctorCommand())
	root.AddCommand(a.changelogCommand())
	root.AddCommand(a.updateCommand())
	root.AddCommand(a.loginCommand())
	root.AddCommand(a.logoutCommand())
	root.AddCommand(a.authFileCommand())
	root.AddCommand(a.quotaCommand())
	root.AddCommand(a.guardCommand())
	root.AddCommand(a.rawCommand())
	installUpdateNoticeHelp(root, a)
	return root
}

func (a *application) validateOutput(cmd *cobra.Command) error {
	if a.jsonAlias {
		formatFlag := cmd.Flags().Lookup("format")
		if formatFlag != nil && formatFlag.Changed && !strings.EqualFold(strings.TrimSpace(a.format), output.FormatJSON) {
			return output.NewError("E_USAGE", "--json conflicts with a non-json --format", nil)
		}
		a.format = output.FormatJSON
	}
	a.format = strings.ToLower(strings.TrimSpace(a.format))
	if a.format == "" {
		a.format = output.FormatJSON
	}
	switch a.format {
	case output.FormatJSON, output.FormatText:
	case output.FormatRaw:
		if cmd.Annotations["raw_output"] != "true" {
			return output.NewError("E_USAGE", cmd.CommandPath()+" does not support --format raw", nil)
		}
	default:
		return output.NewError("E_USAGE", "--format must be one of: json, text, raw", nil)
	}
	if a.compact && a.format != output.FormatJSON {
		return output.NewError("E_USAGE", "--compact requires --format json", nil)
	}
	if len(a.fields) > 0 && a.format != output.FormatJSON {
		return output.NewError("E_USAGE", "--fields requires --format json", nil)
	}
	if a.dryRun && strings.TrimSpace(a.confirm) != "" {
		return output.NewError("E_USAGE", "--dry-run and --confirm are mutually exclusive", nil)
	}
	return nil
}

func (a *application) printer() *output.Printer {
	return output.NewPrinter(a.out, output.Options{
		Format:    a.format,
		Compact:   a.compact,
		Fields:    a.fields,
		StartedAt: a.startedAt,
		Notices:   a.cachedNotices(),
	})
}

func (a *application) success(data any) error {
	if err := a.printer().Success(data); err != nil {
		if len(a.fields) > 0 {
			return output.WrapError("E_VALIDATION", "invalid --fields selection: "+err.Error(), err, nil)
		}
		return output.WrapError("E_UNKNOWN", "failed to encode command output", err, nil)
	}
	return nil
}

func (a *application) runtimeConfig(requireCredential bool) (config.Config, error) {
	cfg, err := config.Load(config.LoadOptions{
		BaseURL:          a.baseURL,
		StateDir:         a.stateDir,
		Timeout:          a.timeout,
		ReadKeyFromStdin: a.managementStdin,
		Stdin:            a.in,
		CredentialStore:  a.store,
	})
	if err != nil {
		return config.Config{}, output.WrapError("E_CONFIG", err.Error(), err, nil)
	}
	if requireCredential && strings.TrimSpace(cfg.ManagementKey) == "" {
		return config.Config{}, output.NewError("E_CONFIG", "management key is not configured", map[string]any{
			"required_env": config.EnvManagementKey,
			"login":        "cliproxyapi-cli login",
		})
	}
	return cfg, nil
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

func asCLIError(err error) *output.CLIError {
	var cliErr *output.CLIError
	if errors.As(err, &cliErr) {
		return cliErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return output.WrapError("E_TIMEOUT", "operation timed out", err, nil)
	}
	if errors.Is(err, context.Canceled) {
		return output.WrapError("E_INTERRUPTED", "operation was interrupted", err, nil)
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		message := "management API request failed"
		if apiErr.StatusCode == 0 && strings.TrimSpace(apiErr.Message) != "" {
			message = apiErr.Message
		}
		details := map[string]any{}
		if apiErr.StatusCode > 0 {
			details["status_code"] = apiErr.StatusCode
		}
		return output.WrapError(apiErr.Code, message, err, details)
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return output.WrapError("E_TIMEOUT", "management API request timed out", err, nil)
		}
		return output.WrapError("E_NETWORK", "management API network request failed", err, nil)
	}
	return output.WrapError("E_USAGE", err.Error(), err, nil)
}
