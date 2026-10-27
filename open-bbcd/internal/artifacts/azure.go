package artifacts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

// azureStore implements ArtifactStore against Azure Blob Storage using
// Shared Key (account name + account key) auth. The framework-computed
// URI (sha256/<hex>) is used verbatim as the blob name inside one
// deployer-provisioned container.
//
// PreferredDelivery is DeliverySignedURL, mirroring s3_compatible: reads
// are a 302 to a read-only Blob service SAS signed locally with the
// account key, carrying SignOptions as rsct/rscd response overrides.
type azureStore struct {
	// containerURL is endpoint+container with no trailing slash. Blob
	// clients are built per call from it; the azblob container package is
	// deliberately not imported (it drags Apache Arrow into the binary).
	containerURL  string
	cred          *blob.SharedKeyCredential
	clientOpts    *azcore.ClientOptions // may be nil
	account       string
	containerName string
	// insecure is true when ENDPOINT is plain http (Azurite); SAS URLs
	// then allow http as well as https.
	insecure bool
	signTTL  time.Duration
}

// azureBlobKind is the kind string that appears in ARTIFACT_STORE_<ID>_KIND.
const azureBlobKind = "azure_blob"

// sasClockSkew backdates SAS start times so a daemon clock slightly ahead
// of Azure's does not mint URLs that are not yet valid.
const sasClockSkew = 5 * time.Minute

// NewAzureBlob constructs an azure_blob adapter from parsed env-var
// options. Required option keys: ACCOUNT_NAME, ACCOUNT_KEY (base64),
// CONTAINER. Optional: ENDPOINT (defaults to
// https://<ACCOUNT_NAME>.blob.core.windows.net/; plain http only for
// loopback or single-label hosts such as Azurite).
func NewAzureBlob(options map[string]string, signTTL time.Duration) (ArtifactStore, error) {
	return newAzureBlob(options, signTTL, nil)
}

// newAzureBlob is NewAzureBlob with injectable client options so tests
// can swap the HTTP transport and disable retries.
func newAzureBlob(options map[string]string, signTTL time.Duration, clientOpts *azcore.ClientOptions) (*azureStore, error) {
	account := strings.TrimSpace(options["ACCOUNT_NAME"])
	key := strings.TrimSpace(options["ACCOUNT_KEY"])
	containerName := strings.TrimSpace(options["CONTAINER"])

	if account == "" {
		return nil, fmt.Errorf("azure_blob: ACCOUNT_NAME is required")
	}
	if key == "" {
		return nil, fmt.Errorf("azure_blob: ACCOUNT_KEY is required")
	}
	if containerName == "" {
		return nil, fmt.Errorf("azure_blob: CONTAINER is required")
	}
	// Never echo the key: the decode error alone is enough to act on.
	if _, err := base64.StdEncoding.DecodeString(key); err != nil {
		return nil, fmt.Errorf("azure_blob: ACCOUNT_KEY is not valid base64")
	}
	if signTTL <= 0 {
		return nil, fmt.Errorf("azure_blob: signed-URL TTL must be positive, got %v", signTTL)
	}

	endpoint, insecure, err := azureEndpoint(options["ENDPOINT"], account)
	if err != nil {
		return nil, err
	}

	cred, err := blob.NewSharedKeyCredential(account, key)
	if err != nil {
		return nil, fmt.Errorf("azure_blob: ACCOUNT_KEY rejected by the SDK")
	}

	return &azureStore{
		containerURL:  endpoint + containerName,
		cred:          cred,
		clientOpts:    clientOpts,
		account:       account,
		containerName: containerName,
		insecure:      insecure,
		signTTL:       signTTL,
	}, nil
}

// azureEndpoint resolves ENDPOINT to a slash-terminated blob service URL.
// Empty means the public-cloud default for the account. Plain http is
// accepted only for loopback or single-label hosts (Azurite on the host
// or as a compose service) so account-key traffic never crosses a
// network in the clear.
func azureEndpoint(raw, account string) (endpoint string, insecure bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "https://" + account + ".blob.core.windows.net/", false, nil
	}
	u, perr := url.Parse(raw)
	if perr != nil || u.Host == "" {
		return "", false, fmt.Errorf("azure_blob: ENDPOINT %q is not an absolute http(s) URL", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", false, fmt.Errorf("azure_blob: ENDPOINT %q must not contain a query or fragment", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocalHost(u.Hostname()) {
			return "", false, fmt.Errorf("azure_blob: ENDPOINT %q uses plain http to a non-local host (http is allowed only for loopback or single-label hosts such as Azurite)", raw)
		}
		insecure = true
	default:
		return "", false, fmt.Errorf("azure_blob: ENDPOINT %q: unsupported scheme (want https://, or http:// to a local emulator)", raw)
	}
	if !strings.HasSuffix(raw, "/") {
		raw += "/"
	}
	return raw, insecure, nil
}

func isLocalHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return !strings.Contains(host, ".")
}

// blobURL builds the blob URL. Blob names are always sha256/<hex> or
// _probe/<hex>, so no path escaping is needed.
func (s *azureStore) blobURL(uri string) string { return s.containerURL + "/" + uri }

func (s *azureStore) blobClient(uri string) (*blob.Client, error) {
	var o *blob.ClientOptions
	if s.clientOpts != nil {
		o = &blob.ClientOptions{ClientOptions: *s.clientOpts}
	}
	return blob.NewClientWithSharedKeyCredential(s.blobURL(uri), s.cred, o)
}

func (s *azureStore) blockBlobClient(uri string) (*blockblob.Client, error) {
	var o *blockblob.ClientOptions
	if s.clientOpts != nil {
		o = &blockblob.ClientOptions{ClientOptions: *s.clientOpts}
	}
	return blockblob.NewClientWithSharedKeyCredential(s.blobURL(uri), s.cred, o)
}

func (s *azureStore) Kind() string                    { return azureBlobKind }
func (s *azureStore) PreferredDelivery() DeliveryMode { return DeliverySignedURL }

func (s *azureStore) Put(ctx context.Context, uri, mime string, r io.Reader, size int64) (PutResult, error) {
	bb, err := s.blockBlobClient(uri)
	if err != nil {
		return PutResult{}, err
	}
	cr := &countingReader{r: r}
	_, err = bb.UploadStream(ctx, cr, &blockblob.UploadStreamOptions{
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &mime},
	})
	if err != nil {
		return PutResult{}, err
	}
	if cr.n != size {
		return PutResult{}, fmt.Errorf("azure_blob: put %s: read %d bytes, declared %d", uri, cr.n, size)
	}
	return PutResult{
		URI:       uri,
		SizeBytes: size,
		Sha256:    strings.TrimPrefix(uri, "sha256/"),
	}, nil
}

// countingReader counts bytes read so Put can verify the declared size.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (s *azureStore) Get(ctx context.Context, uri string) (io.ReadCloser, error) {
	bc, err := s.blobClient(uri)
	if err != nil {
		return nil, err
	}
	resp, err := bc.DownloadStream(ctx, nil)
	if err != nil {
		return nil, mapAzureErr(err)
	}
	return resp.Body, nil
}

// Sign mints a read-only Blob service SAS locally (no network call).
// Like an S3 presign it does not check that the blob exists.
func (s *azureStore) Sign(_ context.Context, uri string, ttl time.Duration, opts SignOptions) (string, error) {
	if ttl <= 0 {
		ttl = s.signTTL
	}
	protocol := sas.ProtocolHTTPS
	if s.insecure {
		protocol = sas.ProtocolHTTPSandHTTP
	}
	now := time.Now().UTC()
	qp, err := sas.BlobSignatureValues{
		Protocol:           protocol,
		StartTime:          now.Add(-sasClockSkew),
		ExpiryTime:         now.Add(ttl),
		Permissions:        (&sas.BlobPermissions{Read: true}).String(),
		ContainerName:      s.containerName,
		BlobName:           uri,
		ContentType:        opts.ContentType,
		ContentDisposition: opts.ContentDisposition,
	}.SignWithSharedKey(s.cred)
	if err != nil {
		return "", fmt.Errorf("azure_blob: sign: %w", err)
	}
	return s.blobURL(uri) + "?" + qp.Encode(), nil
}

func (s *azureStore) Stat(ctx context.Context, uri string) (StatResult, error) {
	bc, err := s.blobClient(uri)
	if err != nil {
		return StatResult{}, err
	}
	props, err := bc.GetProperties(ctx, nil)
	if err != nil {
		// Any 404 — BlobNotFound or ContainerNotFound — is "not there",
		// matching isS3NotFound's lenient NoSuchBucket handling. Probe
		// catches a missing container at boot.
		if isAzureNotFound(err) {
			return StatResult{Exists: false}, nil
		}
		return StatResult{}, err
	}
	// Sha256 comes from the content-addressed URI; it is meaningless for
	// non-sha256 keys such as probe blobs.
	st := StatResult{Exists: true, Sha256: strings.TrimPrefix(uri, "sha256/")}
	if props.ContentType != nil {
		st.MIME = *props.ContentType
	}
	if props.ContentLength != nil {
		st.SizeBytes = *props.ContentLength
	}
	return st, nil
}

// Delete removes the blob and its snapshots. A missing blob is success,
// matching S3 RemoveObject's idempotence.
func (s *azureStore) Delete(ctx context.Context, uri string) error {
	include := blob.DeleteSnapshotsOptionTypeInclude
	bc, err := s.blobClient(uri)
	if err != nil {
		return err
	}
	_, err = bc.Delete(ctx, &blob.DeleteOptions{DeleteSnapshots: &include})
	if err != nil && azureErrorCode(err) != "BlobNotFound" {
		return err
	}
	return nil
}

// Probe round-trips a sentinel blob (put → stat → delete) and checks that
// a random missing key stats as not-found — the same contract as
// s3_compatible. Shared Key is account-wide, so unlike S3 there is no
// extra list permission to grant; a failure names the Azure error code
// plus a hint for the common misconfigurations.
func (s *azureStore) Probe(ctx context.Context) error {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("%w: could not generate probe key: %v", ErrProbeFailed, err)
	}
	key := "_probe/" + hex.EncodeToString(buf)
	payload := "openbbc probe"
	if _, err := s.Put(ctx, key, "application/octet-stream", strings.NewReader(payload), int64(len(payload))); err != nil {
		return s.probeErr("put", err)
	}
	st, err := s.Stat(ctx, key)
	if err != nil {
		// Best-effort cleanup — don't mask the stat error.
		_ = s.Delete(ctx, key)
		return s.probeErr("stat", err)
	}
	if !st.Exists {
		_ = s.Delete(ctx, key)
		return fmt.Errorf("%w: stat of just-written probe blob %q reported not-found (account %q, container %q)", ErrProbeFailed, key, s.account, s.containerName)
	}
	if err := s.Delete(ctx, key); err != nil {
		return s.probeErr("delete", err)
	}
	missing := "_probe/missing-" + hex.EncodeToString(buf)
	st, err = s.Stat(ctx, missing)
	if err != nil {
		return s.probeErr("stat of missing key", err)
	}
	if st.Exists {
		return fmt.Errorf("%w: stat of random missing key %q reported it exists (account %q, container %q)", ErrProbeFailed, missing, s.account, s.containerName)
	}
	return nil
}

// azureErrHints maps the Azure error codes a misconfigured deployment
// most often hits to an actionable hint for the boot log.
var azureErrHints = map[string]string{
	"AuthenticationFailed":               "wrong or rotated ACCOUNT_KEY",
	"AuthorizationFailure":               "the storage-account firewall or network rules block this daemon",
	"KeyBasedAuthenticationNotPermitted": "Shared Key access is disabled on the account (allowSharedKeyAccess=false)",
	"ContainerNotFound":                  "the CONTAINER must exist; create it before booting",
}

func (s *azureStore) probeErr(op string, err error) error {
	code := azureErrorCode(err)
	if hint, ok := azureErrHints[code]; ok {
		return fmt.Errorf("%w: %s failed on account %q container %q: %s (%s): %v", ErrProbeFailed, op, s.account, s.containerName, code, hint, err)
	}
	return fmt.Errorf("%w: %s failed on account %q container %q (code %q): %v", ErrProbeFailed, op, s.account, s.containerName, code, err)
}

// azureErrorCode extracts the x-ms-error-code from an SDK error, or ""
// when err is not an Azure response error (e.g. a transport failure).
func azureErrorCode(err error) string {
	var re *azcore.ResponseError
	if errors.As(err, &re) {
		return re.ErrorCode
	}
	return ""
}

func isAzureNotFound(err error) bool {
	var re *azcore.ResponseError
	return errors.As(err, &re) && re.StatusCode == http.StatusNotFound
}

// mapAzureErr normalises SDK errors into the sentinels in store.go so the
// retrieval handler can tell "blob gone" (410) from "store unhealthy" (502).
// Any 404 maps to ErrBlobMissing, including ContainerNotFound. This leniency
// is deliberate and mirrors Stat and isS3NotFound; a missing container is
// caught by Probe at boot.
func mapAzureErr(err error) error {
	if err == nil {
		return nil
	}
	if isAzureNotFound(err) {
		return fmt.Errorf("%w: %v", ErrBlobMissing, err)
	}
	return err
}
