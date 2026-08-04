package dockerapi

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// fakeDaemon serves canned handlers on a unix socket and records requests.
// The socket lives in a short temp dir rather than t.TempDir(): that one
// embeds the test's name, and a sockaddr_un path is capped at ~104 bytes.
func fakeDaemon(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return sock
}

func TestContainerCreate_SendsHardeningAndName(t *testing.T) {
	var got struct {
		method, path, query string
		body                map[string]any
	}
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"abc123"}`))
	})
	c := New(sock)
	pids := int64(512)
	id, err := c.ContainerCreate(t.Context(), "caesar-x-y-1", ContainerConfig{
		Image: "img@sha256:deadbeef", User: "501:20", Labels: map[string]string{"caesar.managed": "true"},
	}, HostConfig{
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		ReadonlyRootfs: true, PidsLimit: &pids,
	}, map[string]EndpointSettings{"caesar-x-y-1-net": {Aliases: []string{"egress"}}})
	if err != nil || id != "abc123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if got.method != http.MethodPost || got.path != "/v1.43/containers/create" || got.query != "name=caesar-x-y-1" {
		t.Fatalf("request: %s %s?%s", got.method, got.path, got.query)
	}
	host := got.body["HostConfig"].(map[string]any)
	if host["ReadonlyRootfs"] != true {
		t.Fatal("ReadonlyRootfs not sent")
	}
	nets := got.body["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)
	if _, ok := nets["caesar-x-y-1-net"]; !ok {
		t.Fatal("endpoints config missing attempt network")
	}
}

func TestErrors_NotFoundAndConflictAreTyped(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such container"}`))
	})
	c := New(sock)
	_, err := c.ContainerInspect(t.Context(), "nope")
	if !IsNotFound(err) {
		t.Fatalf("want typed not-found, got %v", err)
	}
	if IsConflict(err) {
		t.Fatal("not-found must not be conflict")
	}
}

func TestContainerWait_ReturnsStatusCode(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("condition") != "not-running" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"StatusCode":137}`))
	})
	c := New(sock)
	code, err := c.ContainerWait(t.Context(), "abc")
	if err != nil || code != 137 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestImagePull_SurfacesStreamErrors(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"Pulling"}` + "\n" + `{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}` + "\n"))
	})
	c := New(sock)
	if err := c.ImagePull(t.Context(), "ghcr.io/nope/nope:latest"); err == nil {
		t.Fatal("want error from pull stream")
	}
}

func TestContainerListByLabel_EncodesFilter(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var f map[string][]string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &f); err != nil || f["label"][0] != "caesar.managed=true" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"Id":"c1","Names":["/caesar-a-b-1"],"State":"exited","Created":100}]`))
	})
	c := New(sock)
	list, err := c.ContainerListByLabel(t.Context(), "caesar.managed=true")
	if err != nil || len(list) != 1 || list[0].ID != "c1" {
		t.Fatalf("list=%v err=%v", list, err)
	}
}

func TestErrors_ConflictIsTyped(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"network with name caesar-x-net already exists"}`))
	})
	c := New(sock)
	_, err := c.NetworkCreate(t.Context(), "caesar-x-net", true, nil)
	if !IsConflict(err) {
		t.Fatalf("want typed conflict, got %v", err)
	}
	if IsNotFound(err) {
		t.Fatal("conflict must not be not-found")
	}
}

// The remaining endpoints are thin wrappers over the same transport: what is
// worth pinning is the request each one issues and the fields it decodes.
func TestEndpoints_IssueTheRequestsTheDaemonExpects(t *testing.T) {
	labelQuery := url.Values{"filters": {`{"label":["caesar.managed=true"]}`}}.Encode()

	tests := []struct {
		name       string
		respStatus int
		respBody   string
		call       func(t *testing.T, c *Client) error
		wantMethod string
		wantPath   string
		wantQuery  string
		wantBody   map[string]any
	}{
		{
			name:     "ping asks the pinned api version",
			respBody: "OK",
			call: func(_ *testing.T, c *Client) error {
				return c.Ping(t.Context())
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1.43/_ping",
		},
		{
			name:     "image inspect reports the digest and the baked-in user",
			respBody: `{"Id":"sha256:aa","Config":{"User":"501:20"},"RepoDigests":["ghcr.io/acme/agent@sha256:bb"]}`,
			call: func(t *testing.T, c *Client) error {
				img, err := c.ImageInspect(t.Context(), "ghcr.io/acme/agent:1")
				if err != nil {
					return err
				}
				want := ImageInspect{ID: "sha256:aa", RepoDigests: []string{"ghcr.io/acme/agent@sha256:bb"}}
				want.Config.User = "501:20"
				if diff := cmp.Diff(want, img); diff != "" {
					t.Fatalf("image (-want +got):\n%s", diff)
				}
				return nil
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1.43/images/ghcr.io/acme/agent:1/json",
		},
		{
			name:     "image pull names the reference and drains the progress stream",
			respBody: `{"status":"Pulling"}` + "\n" + `{"status":"Downloaded"}` + "\n",
			call: func(_ *testing.T, c *Client) error {
				return c.ImagePull(t.Context(), "ghcr.io/acme/agent:1")
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1.43/images/create",
			wantQuery:  url.Values{"fromImage": {"ghcr.io/acme/agent:1"}}.Encode(),
		},
		{
			name:       "network create asks for an internal, labelled, non-duplicate network",
			respStatus: http.StatusCreated,
			respBody:   `{"Id":"net1"}`,
			call: func(t *testing.T, c *Client) error {
				id, err := c.NetworkCreate(t.Context(), "caesar-x-net", true, map[string]string{"caesar.managed": "true"})
				if err != nil {
					return err
				}
				if id != "net1" {
					t.Fatalf("id=%q", id)
				}
				return nil
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1.43/networks/create",
			wantBody: map[string]any{
				"Name":           "caesar-x-net",
				"Internal":       true,
				"Labels":         map[string]any{"caesar.managed": "true"},
				"CheckDuplicate": true,
			},
		},
		{
			name:       "network remove deletes by id",
			respStatus: http.StatusNoContent,
			call: func(_ *testing.T, c *Client) error {
				return c.NetworkRemove(t.Context(), "net1")
			},
			wantMethod: http.MethodDelete,
			wantPath:   "/v1.43/networks/net1",
		},
		{
			name:     "network inspect resolves a name to an id",
			respBody: `{"Id":"net1","Name":"caesar-x-net"}`,
			call: func(t *testing.T, c *Client) error {
				net, err := c.NetworkInspect(t.Context(), "caesar-x-net")
				if err != nil {
					return err
				}
				if diff := cmp.Diff(NetworkInspect{ID: "net1", Name: "caesar-x-net"}, net); diff != "" {
					t.Fatalf("network (-want +got):\n%s", diff)
				}
				return nil
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1.43/networks/caesar-x-net",
		},
		{
			name:       "container start posts an empty start",
			respStatus: http.StatusNoContent,
			call: func(_ *testing.T, c *Client) error {
				return c.ContainerStart(t.Context(), "abc")
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1.43/containers/abc/start",
		},
		{
			name:       "container kill carries the signal",
			respStatus: http.StatusNoContent,
			call: func(_ *testing.T, c *Client) error {
				return c.ContainerKill(t.Context(), "abc", "SIGKILL")
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1.43/containers/abc/kill",
			wantQuery:  url.Values{"signal": {"SIGKILL"}}.Encode(),
		},
		{
			name:       "container remove always takes the anonymous volumes with it",
			respStatus: http.StatusNoContent,
			call: func(_ *testing.T, c *Client) error {
				return c.ContainerRemove(t.Context(), "abc", true)
			},
			wantMethod: http.MethodDelete,
			wantPath:   "/v1.43/containers/abc",
			wantQuery:  url.Values{"force": {"1"}, "v": {"1"}}.Encode(),
		},
		{
			name:     "container inspect reports the terminal state the runner reads",
			respBody: `{"Id":"abc","State":{"Status":"exited","ExitCode":137,"OOMKilled":true,"Error":"","StartedAt":"2026-08-04T10:00:00Z","FinishedAt":"2026-08-04T10:00:09Z"},"Config":{"Env":["A=b"],"Image":"img","Labels":{"caesar.managed":"true"}}}`,
			call: func(t *testing.T, c *Client) error {
				got, err := c.ContainerInspect(t.Context(), "abc")
				if err != nil {
					return err
				}
				var want ContainerInspect
				want.ID = "abc"
				want.State.Status = "exited"
				want.State.ExitCode = 137
				want.State.OOMKilled = true
				want.State.StartedAt = "2026-08-04T10:00:00Z"
				want.State.FinishedAt = "2026-08-04T10:00:09Z"
				want.Config.Env = []string{"A=b"}
				want.Config.Image = "img"
				want.Config.Labels = map[string]string{"caesar.managed": "true"}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Fatalf("container (-want +got):\n%s", diff)
				}
				return nil
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1.43/containers/abc/json",
		},
		{
			name:     "container logs streams both streams untailed and untimestamped",
			respBody: "framed-bytes",
			call: func(t *testing.T, c *Client) error {
				body, err := c.ContainerLogs(t.Context(), "abc", false)
				if err != nil {
					return err
				}
				defer body.Close()
				raw, err := io.ReadAll(body)
				if err != nil {
					return err
				}
				if string(raw) != "framed-bytes" {
					t.Fatalf("body=%q", raw)
				}
				return nil
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1.43/containers/abc/logs",
			wantQuery:  url.Values{"follow": {"0"}, "stderr": {"1"}, "stdout": {"1"}, "timestamps": {"0"}}.Encode(),
		},
		{
			name:     "container logs follows when asked",
			respBody: "framed-bytes",
			call: func(_ *testing.T, c *Client) error {
				body, err := c.ContainerLogs(t.Context(), "abc", true)
				if err != nil {
					return err
				}
				defer body.Close()
				_, err = io.Copy(io.Discard, body)
				return err
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1.43/containers/abc/logs",
			wantQuery:  url.Values{"follow": {"1"}, "stderr": {"1"}, "stdout": {"1"}, "timestamps": {"0"}}.Encode(),
		},
		{
			name:     "network list filters by label",
			respBody: `[{"Id":"net1","Name":"caesar-x-net","Created":"2026-08-04T10:00:00Z","Labels":{"caesar.managed":"true"}}]`,
			call: func(t *testing.T, c *Client) error {
				list, err := c.NetworkListByLabel(t.Context(), "caesar.managed=true")
				if err != nil {
					return err
				}
				want := []NetworkSummary{{
					ID: "net1", Name: "caesar-x-net", Created: "2026-08-04T10:00:00Z",
					Labels: map[string]string{"caesar.managed": "true"},
				}}
				if diff := cmp.Diff(want, list); diff != "" {
					t.Fatalf("networks (-want +got):\n%s", diff)
				}
				return nil
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1.43/networks",
			wantQuery:  labelQuery,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got struct {
				method, path, query string
				body                map[string]any
			}
			sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.RawQuery
				_ = json.NewDecoder(r.Body).Decode(&got.body)
				if tt.respStatus != 0 {
					w.WriteHeader(tt.respStatus)
				}
				_, _ = io.WriteString(w, tt.respBody)
			})
			if err := tt.call(t, New(sock)); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got.method != tt.wantMethod || got.path != tt.wantPath || got.query != tt.wantQuery {
				t.Fatalf("request: %s %s?%s, want: %s %s?%s",
					got.method, got.path, got.query, tt.wantMethod, tt.wantPath, tt.wantQuery)
			}
			if tt.wantBody != nil {
				if diff := cmp.Diff(tt.wantBody, got.body); diff != "" {
					t.Fatalf("request body (-want +got):\n%s", diff)
				}
			}
		})
	}
}
