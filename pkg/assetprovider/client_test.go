package assetprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/volcsign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderAssetLifecycle(t *testing.T) {
	for _, tc := range []struct{ name, path, auth, kind string }{
		{"compatible bearer", "/openapi/volcengine/", "bearer", "image"},
		{"official signed", "/", "aksk", "video"},
		{"audio", "/", "bearer", "audio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				payload, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) {
					return
				}
				assert.Equal(t, "application/json; charset=utf-8", r.Header.Get("Content-Type"))
				if tc.auth == "bearer" {
					assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
				} else {
					signedAt, err := time.Parse("20060102T150405Z", r.Header.Get("X-Date"))
					if !assert.NoError(t, err) {
						return
					}
					headers, err := volcsign.Sign(volcsign.Request{Method: r.Method, URL: "https://" + r.Host + r.URL.RequestURI(), Body: string(payload), Headers: map[string]string{"content-type": r.Header.Get("Content-Type")}, AccessKey: "test-key", SecretKey: "test-secret", Region: "cn-beijing", Service: "ark"}, signedAt)
					if !assert.NoError(t, err) {
						return
					}
					assert.Equal(t, headers["Authorization"], r.Header.Get("Authorization"))
					assert.Equal(t, headers["X-Content-Sha256"], r.Header.Get("X-Content-Sha256"))
				}
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, tc.path, r.URL.Path)
				assert.Equal(t, "2024-01-01", r.URL.Query().Get("Version"))
				var body map[string]any
				if !assert.NoError(t, common.Unmarshal(payload, &body)) {
					return
				}
				assert.Equal(t, "my-project", body["ProjectName"])
				assert.NotContains(t, body, "model")
				switch call {
				case 1:
					assert.Equal(t, "CreateAsset", r.URL.Query().Get("Action"))
					assert.Equal(t, "group-test", body["GroupId"])
					assert.Equal(t, strings.ToUpper(tc.kind[:1])+tc.kind[1:], body["AssetType"])
					assert.Equal(t, "https://cdn.example/reference?signature=secret", body["URL"])
					_, _ = io.WriteString(w, `{"Result":{"Id":"asset-test"}}`)
				case 2:
					assert.Equal(t, "GetAsset", r.URL.Query().Get("Action"))
					assert.Equal(t, "asset-test", body["Id"])
					_, _ = io.WriteString(w, `{"Result":{"Id":"asset-test","Status":"Active","URL":"https://private.example/do-not-expose"}}`)
				case 3:
					assert.Equal(t, "DeleteAsset", r.URL.Query().Get("Action"))
					assert.Equal(t, "asset-test", body["Id"])
					_, _ = io.WriteString(w, `{"Result":{}}`)
				}
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL + tc.path, AuthMode: tc.auth, APIKey: "test-key", SecretKey: "test-secret", GroupID: "group-test", ProjectName: "my-project"}, server.Client())
			require.NoError(t, err)
			created, err := client.Create(t.Context(), Source{Name: "reference", Kind: tc.kind, URL: "https://cdn.example/reference?signature=secret"})
			require.NoError(t, err)
			assert.Equal(t, Result{ID: "asset-test", Reference: "asset://asset-test", Status: StatusPending}, created)
			ready, err := client.Get(t.Context(), created.ID)
			require.NoError(t, err)
			assert.Equal(t, Result{ID: "asset-test", Reference: "asset://asset-test", Status: StatusReady}, ready)
			require.NoError(t, client.Delete(t.Context(), created.ID))
			assert.Equal(t, int32(3), calls.Load())
		})
	}
}

func TestCreationFailureSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		uncertain  bool
	}{
		{"server error", `{}`, 500, true},
		{"timeout", `{}`, 408, true},
		{"rate limit", `{}`, 429, false},
		{"unauthorized", `{}`, 401, false},
		{"malformed success", `not json`, 200, true},
		{"missing result", `{}`, 200, true},
		{"missing id", `{"Result":{}}`, 200, true},
		{"invalid result", `{"Result":[]}`, 200, true},
		{"business error", `{"ResponseMetadata":{"Error":{"Code":"InvalidParameter","Message":"test-key test-secret https://cdn.example/private?sig=secret Signature=private"}}}`, 200, false},
		{"internal business error", `{"ResponseMetadata":{"Error":{"Code":"InternalError","Message":"private"}}}`, 200, true},
		{"secret in error code", `{"ResponseMetadata":{"Error":{"Code":"test-key","Message":"private"}}}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key", SecretKey: "test-secret", GroupID: "group-1"}, server.Client())
			require.NoError(t, err)
			for _, create := range []func() error{
				func() error {
					_, err := client.Create(t.Context(), Source{Kind: "image", URL: "https://cdn.example/reference.png"})
					return err
				},
				func() error { _, err := client.CreateGroup(t.Context(), "channel-group"); return err },
			} {
				var providerErr *Error
				require.ErrorAs(t, create(), &providerErr)
				assert.Equal(t, tc.uncertain, providerErr.Uncertain)
				assert.NotContains(t, providerErr.Error(), "test-key")
				assert.NotContains(t, providerErr.Code, "test-key")
				assert.NotContains(t, providerErr.Error(), "test-secret")
				assert.NotContains(t, providerErr.Error(), "https://")
				assert.NotContains(t, providerErr.Error(), "Signature=")
			}
			assert.Equal(t, int32(2), calls.Load(), "creation must never be automatically retried")
		})
	}
}

func TestAssetPollingStatesAndDeletion(t *testing.T) {
	for _, tc := range []struct{ body, wantStatus, wantError string }{
		{`{"Result":{"Id":"asset-1","Status":"Processing"}}`, StatusPending, ""},
		{`{"Result":{"Id":"asset-1","Status":"Failed","Error":{"Code":"ContentRejected","Message":"Not allowed"}}}`, StatusFailed, "ContentRejected: Not allowed"},
		{`{"Result":{"Id":"asset-1","Status":"Failed","Error":{"Message":"test-key https://cdn.example/signed?key=secret"}}}`, StatusFailed, ": [redacted] [redacted URL]"},
		{`{"Result":{"Id":"asset-1","Status":"Failed","Error":{"Message":"Signature=private-signature"}}}`, StatusFailed, "Asset provider rejected the operation"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("Action") == "DeleteAsset" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key", GroupID: "group-1"}, server.Client())
			require.NoError(t, err)
			result, err := client.Get(t.Context(), "asset-1")
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, result.Status)
			assert.Equal(t, tc.wantError, result.Error)
			require.NoError(t, client.Delete(t.Context(), "asset-1"))
		})
	}
}

func TestAssetResultIdentityAndStatusValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		create     bool
	}{
		{"different identifier", `{"Result":{"Id":"another-asset","Status":"Active"}}`, false},
		{"missing status", `{"Result":{"Id":"asset-1"}}`, false},
		{"unknown poll status", `{"Result":{"Id":"asset-1","Status":"unexpected"}}`, false},
		{"unknown import status", `{"Result":{"Id":"asset-1","Status":"unexpected"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key", GroupID: "group-1"}, server.Client())
			require.NoError(t, err)
			if tc.create {
				_, err = client.Create(t.Context(), Source{Kind: "image", URL: "https://cdn.example/a.png"})
			} else {
				_, err = client.Get(t.Context(), "asset-1")
			}
			var providerErr *Error
			require.ErrorAs(t, err, &providerErr)
			assert.Equal(t, tc.create, providerErr.Uncertain)
		})
	}
}

func TestAssetGroupLifecycle(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if !assert.NoError(t, common.DecodeJson(r.Body, &body)) {
			return
		}
		assert.Equal(t, "default", body["ProjectName"])
		assert.Equal(t, http.MethodPost, r.Method)
		switch r.URL.Query().Get("Action") {
		case "CreateAssetGroup":
			assert.Equal(t, "channel-group", body["Name"])
			assert.Equal(t, "AIGC", body["GroupType"])
			_, _ = io.WriteString(w, `{"Result":{"Id":"group-1"}}`)
		case "GetAssetGroup":
			assert.Equal(t, "group-1", body["Id"])
			_, _ = io.WriteString(w, `{"Result":{"Id":"group-1","Name":"channel-group","GroupType":"AIGC","ProjectName":"default"}}`)
		case "DeleteAssetGroup":
			assert.Equal(t, "group-1", body["Id"])
			_, _ = io.WriteString(w, `{"Result":{}}`)
		default:
			t.Error("unexpected action")
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key"}, server.Client())
	require.NoError(t, err)
	_, err = client.Create(t.Context(), Source{Kind: "image", URL: "https://cdn.example/reference.png"})
	require.Error(t, err, "asset imports require an established group")
	assert.Zero(t, calls.Load())
	created, err := client.CreateGroup(t.Context(), "channel-group")
	require.NoError(t, err)
	assert.Equal(t, Group{ID: "group-1", Name: "channel-group", Type: "AIGC", ProjectName: "default"}, created)
	group, err := client.GetGroup(t.Context(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, created, group)
	require.NoError(t, client.DeleteGroup(t.Context(), created.ID))
	assert.Equal(t, int32(3), calls.Load())
}

func TestFindAssetGroupChecksEveryPageAndExactIdentity(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := int(calls.Add(1))
		var body map[string]any
		if !assert.NoError(t, common.DecodeJson(r.Body, &body)) {
			return
		}
		assert.Equal(t, "ListAssetGroups", r.URL.Query().Get("Action"))
		assert.Equal(t, float64(page), body["PageNumber"])
		assert.Equal(t, float64(100), body["PageSize"])
		assert.Equal(t, map[string]any{"Name": "channel-group", "GroupType": "AIGC"}, body["Filter"])
		items := []Group{{ID: "group-exact", Name: "channel-group", Type: "AIGC", ProjectName: "default"}}
		if page == 1 {
			items = make([]Group, 100)
			for i := range items {
				items[i] = Group{ID: fmt.Sprintf("group-%d", i), Name: "channel-group-other", Type: "AIGC", ProjectName: "default"}
			}
			items[0].Name = "channel-group"
			items[0].ProjectName = "another-project"
			items[1].Name = "channel-group"
			items[1].Type = "LivenessFace"
		}
		payload, err := common.Marshal(map[string]any{"Result": map[string]any{"Items": items, "TotalCount": 101, "PageSize": 100, "PageNumber": page}})
		if assert.NoError(t, err) {
			_, _ = w.Write(payload)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key"}, server.Client())
	require.NoError(t, err)
	group, found, err := client.FindGroup(t.Context(), "channel-group")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "group-exact", group.ID)
	assert.Equal(t, int32(2), calls.Load())
}

func TestGroupRecoveryRejectsIncompleteOrAmbiguousResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		absent     bool
	}{
		{"absent", `{"Result":{"Items":[],"TotalCount":0,"PageNumber":1,"PageSize":100}}`, true},
		{"missing pagination", `{"Result":{"Items":[]}}`, false},
		{"missing items", `{"Result":{"TotalCount":0,"PageNumber":1,"PageSize":100}}`, false},
		{"incomplete page", `{"Result":{"Items":[],"TotalCount":1,"PageNumber":1,"PageSize":100}}`, false},
		{"missing project", `{"Result":{"Items":[{"Id":"group-1","Name":"channel-group","GroupType":"AIGC"}],"TotalCount":1,"PageNumber":1,"PageSize":100}}`, false},
		{"duplicate name", `{"Result":{"Items":[{"Id":"group-1","Name":"channel-group","GroupType":"AIGC","ProjectName":"default"},{"Id":"group-2","Name":"channel-group","GroupType":"AIGC","ProjectName":"default"}],"TotalCount":2,"PageNumber":1,"PageSize":100}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key"}, server.Client())
			require.NoError(t, err)
			_, found, err := client.FindGroup(t.Context(), "channel-group")
			assert.False(t, found)
			if tc.absent {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	for _, group := range []Group{
		{ID: "other-group", Name: "channel-group", Type: "AIGC", ProjectName: "default"},
		{ID: "group-1", Name: "channel-group", Type: "LivenessFace", ProjectName: "default"},
		{ID: "group-1", Name: "channel-group", Type: "AIGC", ProjectName: "another-project"},
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload, err := common.Marshal(map[string]any{"Result": group})
			if assert.NoError(t, err) {
				_, _ = w.Write(payload)
			}
		}))
		client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key"}, server.Client())
		require.NoError(t, err)
		_, err = client.GetGroup(t.Context(), "group-1")
		require.Error(t, err)
		server.Close()
	}
}

func TestAssetRequestBoundaries(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("caller redirect policy must not run"); return nil }
	client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key", GroupID: "group-1"}, httpClient)
	require.NoError(t, err)
	_, err = client.Create(t.Context(), Source{Kind: "image", URL: "https://cdn.example/reference.png"})
	require.Error(t, err)
	_, err = client.CreateGroup(t.Context(), "channel-group")
	require.Error(t, err)
	assert.Zero(t, redirected.Load())
	for _, source := range []Source{{Kind: "unknown", URL: "https://cdn.example/a"}, {Kind: "image", URL: "file:///etc/passwd"}, {Kind: "image", URL: "https://user:password@cdn.example/a.png"}} {
		_, err = client.Create(t.Context(), source)
		var providerErr *Error
		require.ErrorAs(t, err, &providerErr)
		assert.False(t, providerErr.Uncertain)
		assert.Zero(t, providerErr.StatusCode)
	}
	for _, id := range []string{"", "..", "../groups", "id?sync=false", "id%2Fgroups", "asset://id", "id\nheader"} {
		_, err := client.Get(t.Context(), id)
		require.Error(t, err)
		require.Error(t, client.Delete(t.Context(), id))
		_, err = client.GetGroup(t.Context(), id)
		require.Error(t, err)
		require.Error(t, client.DeleteGroup(t.Context(), id))
	}
}

func TestAssetDeleteBusinessNotFound(t *testing.T) {
	for _, tc := range []struct {
		code   string
		absent bool
	}{
		{"NotFound", true}, {"NotFound.asset_id", true}, {"NotFound.group_id", true}, {"AccessDenied", false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"ResponseMetadata":{"Error":{"Code":"`+tc.code+`","Message":"Asset not found"}}}`)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, AuthMode: "bearer", APIKey: "test-key"}, server.Client())
			require.NoError(t, err)
			for _, remove := range []func(context.Context, string) error{client.Delete, client.DeleteGroup} {
				err = remove(t.Context(), "resource-1")
				if tc.absent {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			}
		})
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("private request https://assets.example/secret?api_key=test-key")
}

func TestAssetTransportUnknownOutcome(t *testing.T) {
	client, err := New(Config{BaseURL: "https://assets.example", AuthMode: "bearer", APIKey: "test-key", GroupID: "group-1"}, &http.Client{Transport: failingTransport{}})
	require.NoError(t, err)
	_, err = client.Create(t.Context(), Source{Kind: "image", URL: "https://cdn.example/reference.png"})
	var providerErr *Error
	require.ErrorAs(t, err, &providerErr)
	assert.True(t, providerErr.Uncertain)
	assert.NotContains(t, err.Error(), "https://")
	_, err = client.CreateGroup(t.Context(), "channel-group")
	require.ErrorAs(t, err, &providerErr)
	assert.True(t, providerErr.Uncertain)
	_, err = client.Get(t.Context(), "asset-1")
	require.ErrorAs(t, err, &providerErr)
	assert.False(t, providerErr.Uncertain)
}

func TestAssetProviderConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config Config
	}{
		{"embedded credentials", Config{BaseURL: "https://user:secret@assets.example", AuthMode: "bearer", APIKey: "private-test-api-key"}},
		{"query on endpoint", Config{BaseURL: "https://assets.example?key=secret", AuthMode: "bearer", APIKey: "private-test-api-key"}},
		{"missing secret key", Config{BaseURL: "https://assets.example", AuthMode: "aksk", APIKey: "private-test-api-key"}},
		{"header injection", Config{BaseURL: "https://assets.example", AuthMode: "bearer", APIKey: "key\r\nOther: header"}},
		{"unsupported auth", Config{BaseURL: "https://assets.example", AuthMode: "unknown", APIKey: "private-test-api-key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.config, nil)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), tc.config.APIKey)
		})
	}
}
func TestVolcengineSignatureMatchesOfficialSDK(t *testing.T) {
	// Generated with base.GetSignRequest from volcengine/volc-sdk-golang at
	// 64e6e051a12441821178c93b8922db4e7158d3e1. Both the root endpoint and the
	// gateway's trailing-slash endpoint must sign the path actually sent.
	for _, tc := range []struct{ path, signature string }{
		{"/", "b797a8ae450c31e942fa5460e0530e3ec79459508892fbbed1245960ca8c1e39"},
		{"/openapi/volcengine/", "b7aed3dcdec50d449c3583c6c1a560cb2e434c7b9ffc664c4621d8de6483c509"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			headers, err := volcsign.Sign(volcsign.Request{
				Method: "POST", URL: "https://assets.example.com" + tc.path + "?Version=2024-01-01&Action=CreateAsset",
				Body:      `{"AssetType":"Image","GroupId":"group-test","Name":"reference","ProjectName":"default","URL":"https://cdn.example/reference.png"}`,
				Headers:   map[string]string{"content-type": "application/json; charset=utf-8"},
				AccessKey: "test-ak", SecretKey: "test-sk", Region: "cn-beijing", Service: "ark",
			}, time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			assert.Equal(t, "20261019T120000Z", headers["X-Date"])
			assert.Equal(t, "14ed5b70ac00c2cac43899aaf414a624eccc8e2d22586c081c877ea484949b76", headers["X-Content-Sha256"])
			assert.Equal(t, "HMAC-SHA256 Credential=test-ak/20261019/cn-beijing/ark/request, SignedHeaders=content-type;host;x-content-sha256;x-date, Signature="+tc.signature, headers["Authorization"])
		})
	}
}
