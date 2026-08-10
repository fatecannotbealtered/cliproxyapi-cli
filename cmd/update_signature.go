package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	tuffetcher "github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
)

var errTrustRootUnavailable = errors.New("sigstore trust root unavailable")

const (
	updateTUFRefreshTimeout = 30 * time.Second
	updateOIDCIssuer        = "https://token.actions.githubusercontent.com"
)

func updateSignerIdentityRegexp(targetVersion string) string {
	tagPattern := regexp.QuoteMeta("v" + normalizeUpdateVersion(targetVersion))
	return "^https://github\\.com/" + regexp.QuoteMeta(updateDefaultRepo) +
		"/\\.github/workflows/release\\.yml@refs/tags/" + tagPattern + "$"
}

var (
	updateVerifySignature  = verifySigstoreBundle
	updateFetchTrustedRoot = root.FetchTrustedRootWithOptions
)

func updateTrustedRoot(ctx context.Context) (*root.TrustedRoot, error) {
	refreshCtx, cancel := context.WithTimeout(ctx, updateTUFRefreshTimeout)
	defer cancel()

	// DefaultOptions anchors TUF with sigstore-go's embedded root.json; refresh is authenticated, not TOFU.
	opts := tuf.DefaultOptions()
	fetcher := tuffetcher.NewDefaultFetcher()
	fetcher.SetHTTPClient(&http.Client{Timeout: updateTUFRefreshTimeout})
	opts.Fetcher = fetcher

	type result struct {
		tr  *root.TrustedRoot
		err error
	}
	done := make(chan result, 1)
	go func() {
		tr, err := updateFetchTrustedRoot(opts)
		done <- result{tr: tr, err: err}
	}()
	select {
	case <-refreshCtx.Done():
		return nil, fmt.Errorf("%w: refreshing TUF trust metadata: %w", errTrustRootUnavailable, refreshCtx.Err())
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("%w: refreshing TUF trust metadata: %w", errTrustRootUnavailable, r.err)
		}
		return r.tr, nil
	}
}

func verifySigstoreBundle(ctx context.Context, artifactPath, bundlePath, sanRegex string) error {
	b, err := bundle.LoadJSONFromPath(bundlePath)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return &updateLocalIOError{err: fmt.Errorf("loading signature bundle: %w", err)}
		}
		return fmt.Errorf("loading signature bundle: %w", err)
	}
	trustedRoot, err := updateTrustedRoot(ctx)
	if err != nil {
		return err
	}
	sev, err := verify.NewVerifier(trustedRoot,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("building sigstore verifier: %w", err)
	}
	certID, err := verify.NewShortCertificateIdentity(updateOIDCIssuer, "", "", sanRegex)
	if err != nil {
		return fmt.Errorf("building certificate identity policy: %w", err)
	}
	artifact, err := os.Open(artifactPath)
	if err != nil {
		return &updateLocalIOError{err: fmt.Errorf("opening signed artifact: %w", err)}
	}
	defer func() { _ = artifact.Close() }()
	if _, err := sev.Verify(b, verify.NewPolicy(
		verify.WithArtifact(artifact),
		verify.WithCertificateIdentity(certID),
	)); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	return nil
}
