package selfupdate

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestReleaseResponseBound(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, strings.Repeat("x", maxReleaseBytes+1)), nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	source := NewHTTPSource(client)
	if _, err := source.Release(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized release response error = %v", err)
	}
}

func TestReleaseRejectsMalformedDuplicateAndPrereleaseMetadata(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"tag_name":"v0.3.0","assets":[{"name":"a","size":1},{"name":"a","size":1}]}`,
		`{"tag_name":"v0.3.0","prerelease":true,"assets":[]}`,
		`{"tag_name":"v0.3.0-rc.1","assets":[]}`,
	} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, body), nil
		}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if _, err := NewHTTPSource(client).Release(context.Background(), nil); err == nil {
			t.Errorf("accepted release response %q", body)
		}
	}
}

func TestDownloadArchiveBoundUsesMetadataAndBody(t *testing.T) {
	release := ReleaseInfo{Version: Version{Major: 1}, Assets: map[string]ReleaseAsset{"a": {Name: "a", Size: 11}}}
	source := NewHTTPSource(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "12345678901"), nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	if _, err := source.DownloadAssets(context.Background(), release, []AssetRequirement{{Name: "a", MaxBytes: 10}}); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	release.Assets["a"] = ReleaseAsset{Name: "a", Size: 10}
	if _, err := source.DownloadAssets(context.Background(), release, []AssetRequirement{{Name: "a", MaxBytes: 10}}); err == nil {
		t.Fatal("oversized response body accepted")
	}
}

func TestDownloadRequiresAdvertisedExactAsset(t *testing.T) {
	release := ReleaseInfo{Version: Version{Major: 1}, Assets: map[string]ReleaseAsset{}}
	if _, err := NewHTTPSource(nil).DownloadAssets(context.Background(), release, []AssetRequirement{{Name: "missing", MaxBytes: 1}}); err == nil {
		t.Fatal("unadvertised asset accepted")
	}
}

func TestHTTPClientRedirectPolicyIsHTTPSAndHostBounded(t *testing.T) {
	policy := NewHTTPClient().CheckRedirect
	via := []*http.Request{{URL: mustURL(t, "https://github.com/start")}}
	for _, raw := range []string{
		"http://github.com/insecure",
		"https://github.com.evil.example/asset",
		"https://example.com/asset",
	} {
		if err := policy(&http.Request{URL: mustURL(t, raw)}, via); err == nil {
			t.Errorf("redirect to %s was accepted", raw)
		}
	}
	if err := policy(&http.Request{URL: mustURL(t, "https://release-assets.githubusercontent.com/asset")}, via); err != nil {
		t.Fatalf("canonical release redirect refused: %v", err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
