package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	githubAPIBase      = "https://api.github.com/repos/twilight-project/dropin-miner/releases"
	githubDownloadBase = "https://github.com/twilight-project/dropin-miner/releases/download"
	maxReleaseBytes    = 1 << 20
	httpClientTimeout  = 2 * time.Minute
	maxRedirects       = 5
)

// ReleaseInfo is the closed subset of a GitHub release needed by the updater.
type ReleaseInfo struct {
	Version    Version
	Draft      bool
	Prerelease bool
	Assets     map[string]ReleaseAsset
}

type ReleaseAsset struct {
	Name string
	Size int64
}

// AssetRequirement lets each verifier state both what it needs and the bound
// under which the orchestrator may fetch it. A future signature verifier can
// add certificates or signatures without changing the downloader.
type AssetRequirement struct {
	Name     string
	MaxBytes int64
}

// ReleaseVerifier describes all remote inputs before any are fetched.
type ReleaseVerifier interface {
	RequiredAssets(release ReleaseInfo, target Artifact) ([]AssetRequirement, error)
	Verify(ctx context.Context, assets map[string][]byte, target Artifact) error
}

// HTTPSource talks only to the compiled-in canonical GitHub repository. Tests
// inject an HTTP client/transport, not a production URL override.
type HTTPSource struct {
	client       *http.Client
	apiBase      string
	downloadBase string
}

func NewHTTPSource(client *http.Client) *HTTPSource {
	if client == nil {
		client = NewHTTPClient()
	}
	return &HTTPSource{client: client, apiBase: githubAPIBase, downloadBase: githubDownloadBase}
}

// NewHTTPClient has an explicit, HTTPS-only redirect policy even though the
// release channel uses no credentials. GitHub release downloads redirect to
// its dedicated release-assets host; no other cross-origin redirect is used.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpClientTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("selfupdate: too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("selfupdate: refused redirect to non-HTTPS URL %s", req.URL.Redacted())
			}
			host := strings.ToLower(req.URL.Hostname())
			if host != "github.com" && host != "api.github.com" && host != "release-assets.githubusercontent.com" {
				return fmt.Errorf("selfupdate: refused redirect to unexpected host %q", host)
			}
			return nil
		},
	}
}

// Release returns GitHub's latest stable release when requested is nil, or an
// exact stable release otherwise. GitHub's "latest" endpoint excludes drafts
// and prereleases; the checks below keep explicit selection equally strict.
func (s *HTTPSource) Release(ctx context.Context, requested *Version) (ReleaseInfo, error) {
	endpoint := s.apiBase + "/latest"
	if requested != nil {
		endpoint = s.apiBase + "/tags/" + url.PathEscape(requested.Tag())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ReleaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dropin-miner-upgrade")
	resp, err := s.client.Do(req)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("query GitHub release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return ReleaseInfo{}, fmt.Errorf("query GitHub release: HTTP %s", resp.Status)
	}
	raw, err := readBounded(resp.Body, maxReleaseBytes)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("GitHub release response: %w", err)
	}
	var payload struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ReleaseInfo{}, fmt.Errorf("malformed GitHub release response: %w", err)
	}
	v, err := ParseReleaseTag(payload.TagName)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("GitHub release: %w", err)
	}
	if requested != nil && v.Compare(*requested) != 0 {
		return ReleaseInfo{}, fmt.Errorf("GitHub returned %s for requested %s", v.Tag(), requested.Tag())
	}
	if payload.Draft || payload.Prerelease {
		return ReleaseInfo{}, fmt.Errorf("release %s is not a published stable release", v.Tag())
	}
	assets := make(map[string]ReleaseAsset, len(payload.Assets))
	for _, asset := range payload.Assets {
		if !safeAssetName(asset.Name) || asset.Size < 0 {
			return ReleaseInfo{}, fmt.Errorf("release %s has invalid asset metadata for %q", v.Tag(), asset.Name)
		}
		if _, duplicate := assets[asset.Name]; duplicate {
			return ReleaseInfo{}, fmt.Errorf("release %s lists asset %q more than once", v.Tag(), asset.Name)
		}
		assets[asset.Name] = ReleaseAsset{Name: asset.Name, Size: asset.Size}
	}
	return ReleaseInfo{Version: v, Draft: payload.Draft, Prerelease: payload.Prerelease, Assets: assets}, nil
}

func (s *HTTPSource) DownloadAssets(ctx context.Context, release ReleaseInfo, requirements []AssetRequirement) (map[string][]byte, error) {
	out := make(map[string][]byte, len(requirements))
	for _, requirement := range requirements {
		if !safeAssetName(requirement.Name) || requirement.MaxBytes <= 0 {
			return nil, fmt.Errorf("invalid verifier requirement for asset %q", requirement.Name)
		}
		if _, duplicate := out[requirement.Name]; duplicate {
			return nil, fmt.Errorf("verifier requires asset %q more than once", requirement.Name)
		}
		metadata, ok := release.Assets[requirement.Name]
		if !ok {
			return nil, fmt.Errorf("release %s has no asset %q", release.Version.Tag(), requirement.Name)
		}
		if metadata.Size > requirement.MaxBytes {
			return nil, fmt.Errorf("asset %q metadata size %d exceeds %d-byte limit", requirement.Name, metadata.Size, requirement.MaxBytes)
		}
		endpoint := s.downloadBase + "/" + url.PathEscape(release.Version.Tag()) + "/" + url.PathEscape(requirement.Name)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("User-Agent", "dropin-miner-upgrade")
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("download %q: %w", requirement.Name, err)
		}
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("download %q: HTTP %s", requirement.Name, resp.Status)
		}
		data, readErr := readBounded(resp.Body, requirement.MaxBytes)
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("download %q: %w", requirement.Name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("download %q: close response: %w", requirement.Name, closeErr)
		}
		out[requirement.Name] = data
	}
	return out, nil
}

func readBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response exceeds %d-byte limit", max)
	}
	return data, nil
}

func safeAssetName(name string) bool {
	return name != "" && name != "." && name != ".." && path.Base(name) == name && !strings.ContainsAny(name, `\\/`)
}
