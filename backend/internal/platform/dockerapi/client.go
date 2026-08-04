// Package dockerapi is a minimal, typed Docker Engine API client over
// the unix socket — exactly the endpoints the runner consumes, API
// version pinned. Hand-rolled to honor the dependency deny-list: the
// official SDK drags the moby module graph for what is, here, a dozen
// HTTP calls (plan 06 decision 1).
package dockerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

const apiVersion = "v1.43"

// Client talks to one daemon over one unix socket.
type Client struct {
	http *http.Client
}

// New builds a client for the daemon listening on socketPath. The http.Client
// carries no timeout on purpose: container waits and log follows are
// open-ended, and every call's deadline arrives with its context instead.
func New(socketPath string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("docker api: %d: %s", e.Status, e.Message) }

// IsNotFound reports whether err is the daemon answering 404 — the container,
// image or network is not there.
func IsNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// IsConflict reports whether err is the daemon answering 409 — the name is
// already taken, or the object is in a state that forbids the operation.
func IsConflict(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict
}

// do issues one request; 2xx decodes into out (when non-nil), anything
// else becomes a typed apiError carrying the daemon's message.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	resp, err := c.send(ctx, method, path, query, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// stream issues one request and hands the caller an unread 2xx body to
// consume and close — for the endpoints whose answer is a stream, not a
// document.
func (c *Client) stream(ctx context.Context, method, path string, query url.Values) (io.ReadCloser, error) {
	resp, err := c.send(ctx, method, path, query, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// send performs the round trip and turns a non-2xx answer into an apiError;
// on success the response body is still open and belongs to the caller.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, in any) (*http.Response, error) {
	u := "http://docker/" + apiVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker api: %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&msg)
		return nil, &apiError{Status: resp.StatusCode, Message: msg.Message}
	}
	return resp, nil
}

// labelFilter encodes a `key=value` label selector the way the daemon's
// `filters` query parameter expects it.
func labelFilter(kv string) url.Values {
	raw, _ := json.Marshal(map[string][]string{"label": {kv}})
	return url.Values{"filters": {string(raw)}}
}

// Ping checks that the daemon is reachable and speaking the pinned API
// version.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/_ping", nil, nil, nil)
}

// ImageInspect reads a local image: its digest and the user it bakes in.
func (c *Client) ImageInspect(ctx context.Context, ref string) (ImageInspect, error) {
	var out ImageInspect
	err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, &out)
	return out, err
}

// ImagePull pulls ref from its registry. The daemon answers with a JSONL
// progress stream that reports failures in-band, so the pull is only
// successful once the stream ends without an error line.
func (c *Client) ImagePull(ctx context.Context, ref string) error {
	body, err := c.stream(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {ref}})
	if err != nil {
		return err
	}
	defer body.Close()
	dec := json.NewDecoder(body)
	for {
		var line struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("docker api: pull %s: progress stream: %w", ref, err)
		}
		if line.Error != "" {
			return fmt.Errorf("docker api: pull %s: %s", ref, line.Error)
		}
	}
}

// NetworkCreate creates a network and returns its id.
func (c *Client) NetworkCreate(ctx context.Context, name string, internal bool, labels map[string]string) (string, error) {
	in := struct {
		Name           string            `json:"Name"`
		Internal       bool              `json:"Internal"`
		Labels         map[string]string `json:"Labels"`
		CheckDuplicate bool              `json:"CheckDuplicate"`
	}{Name: name, Internal: internal, Labels: labels, CheckDuplicate: true}
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/networks/create", nil, in, &out)
	return out.ID, err
}

// NetworkRemove deletes a network by id.
func (c *Client) NetworkRemove(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/networks/"+id, nil, nil, nil)
}

// NetworkInspect reads a network by name or id.
func (c *Client) NetworkInspect(ctx context.Context, name string) (NetworkInspect, error) {
	var out NetworkInspect
	err := c.do(ctx, http.MethodGet, "/networks/"+name, nil, nil, &out)
	return out, err
}

// ContainerCreate creates a named container and returns its id. The daemon
// wants the container config inline with the host and networking configs
// beside it, hence the flattened request body.
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg ContainerConfig, host HostConfig, networks map[string]EndpointSettings) (string, error) {
	type networkingConfig struct {
		EndpointsConfig map[string]EndpointSettings `json:"EndpointsConfig"`
	}
	in := struct {
		ContainerConfig
		HostConfig       HostConfig       `json:"HostConfig"`
		NetworkingConfig networkingConfig `json:"NetworkingConfig"`
	}{
		ContainerConfig:  cfg,
		HostConfig:       host,
		NetworkingConfig: networkingConfig{EndpointsConfig: networks},
	}
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, in, &out)
	return out.ID, err
}

// ContainerStart starts a created container.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
}

// ContainerWait blocks until the container is no longer running and returns
// its exit code. The call is open-ended by design — the deadline is the
// caller's context, never a client timeout.
func (c *Client) ContainerWait(ctx context.Context, id string) (int64, error) {
	var out struct {
		StatusCode int64 `json:"StatusCode"`
		Error      struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+id+"/wait", url.Values{"condition": {"not-running"}}, nil, &out); err != nil {
		return 0, err
	}
	if out.Error.Message != "" {
		return out.StatusCode, fmt.Errorf("docker api: wait %s: %s", id, out.Error.Message)
	}
	return out.StatusCode, nil
}

// ContainerKill sends a signal to a running container.
func (c *Client) ContainerKill(ctx context.Context, id, signal string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/kill", url.Values{"signal": {signal}}, nil, nil)
}

// ContainerRemove deletes a container. Anonymous volumes always go with it:
// nothing this client creates is meant to outlive its container.
func (c *Client) ContainerRemove(ctx context.Context, id string, force bool) error {
	return c.do(ctx, http.MethodDelete, "/containers/"+id, url.Values{
		"v":     {"1"},
		"force": {boolParam(force)},
	}, nil, nil)
}

// ContainerInspect reads a container by id or name.
func (c *Client) ContainerInspect(ctx context.Context, idOrName string) (ContainerInspect, error) {
	var out ContainerInspect
	err := c.do(ctx, http.MethodGet, "/containers/"+idOrName+"/json", nil, nil, &out)
	return out, err
}

// ContainerLogs opens the container's log stream. The body is stdcopy-framed
// (see DemuxLogs) and belongs to the caller, who closes it.
func (c *Client) ContainerLogs(ctx context.Context, id string, follow bool) (io.ReadCloser, error) {
	return c.stream(ctx, http.MethodGet, "/containers/"+id+"/logs", url.Values{
		"stdout":     {"1"},
		"stderr":     {"1"},
		"follow":     {boolParam(follow)},
		"timestamps": {"0"},
	})
}

// ContainerListByLabel lists containers in any state carrying the given
// `key=value` label.
func (c *Client) ContainerListByLabel(ctx context.Context, kv string) ([]ContainerSummary, error) {
	query := labelFilter(kv)
	query.Set("all", "1")
	var out []ContainerSummary
	err := c.do(ctx, http.MethodGet, "/containers/json", query, nil, &out)
	return out, err
}

// NetworkListByLabel lists networks carrying the given `key=value` label.
func (c *Client) NetworkListByLabel(ctx context.Context, kv string) ([]NetworkSummary, error) {
	var out []NetworkSummary
	err := c.do(ctx, http.MethodGet, "/networks", labelFilter(kv), nil, &out)
	return out, err
}

func boolParam(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
