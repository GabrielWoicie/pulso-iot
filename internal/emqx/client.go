// Package emqx implements the EMQX Community 5.8 REST contract. No response body is logged.
package emqx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const auth = "/authentication/password_based%3Abuilt_in_database"
const acl = "/authorization/sources/built_in_database/rules/clients/"

type Client struct {
	URL, Key, Secret string
	HTTP             *http.Client
}
type APIError struct{ Status int }

func (e *APIError) Error() string { return fmt.Sprintf("EMQX HTTP %d", e.Status) }
func New(u, k, s string) *Client {
	return &Client{strings.TrimRight(u, "/"), k, s, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Call(ctx context.Context, method, path string, body, out any) error {
	var b bytes.Buffer
	if body != nil {
		if e := json.NewEncoder(&b).Encode(body); e != nil {
			return e
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, c.URL+"/api/v5"+path, &b)
	if e != nil {
		return fmt.Errorf("EMQX request inválido")
	}
	r.SetBasicAuth(c.Key, c.Secret)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	res, e := c.HTTP.Do(r)
	if e != nil {
		return fmt.Errorf("EMQX indisponível")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &APIError{res.StatusCode}
	}
	if out != nil && res.StatusCode != 204 {
		if e = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out); e != nil {
			return fmt.Errorf("resposta EMQX inválida")
		}
	}
	return nil
}
func isStatus(e error, s int) bool { x, ok := e.(*APIError); return ok && x.Status == s }

// Preflight is read-only. Global settings are never changed implicitly.
func (c *Client) Preflight(ctx context.Context) error {
	var nodes []struct {
		Version string `json:"version"`
	}
	if e := c.Call(ctx, "GET", "/nodes", nil, &nodes); e != nil {
		return e
	}
	if len(nodes) == 0 {
		return fmt.Errorf("EMQX sem nós")
	}
	for _, n := range nodes {
		if !strings.HasPrefix(n.Version, "5.8.9") {
			return fmt.Errorf("EMQX esperado: Community 5.8.9")
		}
	}
	var a struct {
		UserID string `json:"user_id_type"`
		Enable bool   `json:"enable"`
	}
	if e := c.Call(ctx, "GET", auth, nil, &a); e != nil {
		return e
	}
	if a.UserID != "clientid" || !a.Enable {
		return fmt.Errorf("EMQX exige autenticador clientid habilitado")
	}
	var s struct {
		NoMatch string `json:"no_match"`
	}
	if e := c.Call(ctx, "GET", "/authorization/settings", nil, &s); e != nil {
		return e
	}
	if s.NoMatch != "deny" {
		return fmt.Errorf("EMQX exige no_match=deny")
	}
	var sources struct {
		Sources []struct {
			Type   string `json:"type"`
			Enable bool   `json:"enable"`
		} `json:"sources"`
	}
	if e := c.Call(ctx, "GET", "/authorization/sources", nil, &sources); e != nil {
		return e
	}
	found := false
	for _, v := range sources.Sources {
		if v.Enable && v.Type != "built_in_database" {
			return fmt.Errorf("fonte de autorização adicional exige revisão manual")
		}
		found = found || v.Type == "built_in_database" && v.Enable
	}
	if !found {
		return fmt.Errorf("ACL built_in_database ausente")
	}
	return nil
}
func (c *Client) Online(ctx context.Context, id string) (bool, error) {
	var v struct {
		Connected bool `json:"connected"`
	}
	e := c.Call(ctx, "GET", "/clients/"+url.PathEscape(id), nil, &v)
	if isStatus(e, 404) {
		return false, nil
	}
	return v.Connected, e
}
func (c *Client) Kick(ctx context.Context, id string) error {
	e := c.Call(ctx, "DELETE", "/clients/"+url.PathEscape(id), nil, nil)
	if isStatus(e, 404) {
		return nil
	}
	return e
}
func (c *Client) UserExists(ctx context.Context, id string) (bool, error) {
	e := c.Call(ctx, "GET", auth+"/users/"+url.PathEscape(id), nil, nil)
	if isStatus(e, 404) {
		return false, nil
	}
	return e == nil, e
}
func (c *Client) Provision(ctx context.Context, id, password string, replace bool) error {
	exists, e := c.UserExists(ctx, id)
	if e != nil {
		return e
	}
	if exists && !replace {
		return fmt.Errorf("identidade preexistente exige importação explícita da senha atual")
	}
	rules := []map[string]any{}
	for _, v := range []struct{ Kind, Action string }{{"telemetry", "publish"}, {"commands", "subscribe"}, {"command-responses", "publish"}} {
		rules = append(rules, map[string]any{"topic": "eq desbravatech/v1/devices/" + id + "/" + v.Kind, "action": v.Action, "permission": "allow", "qos": []int{0, 1, 2}, "retain": false})
	}
	path := acl + url.PathEscape(id)
	body := map[string]any{"clientid": id, "rules": rules}
	e = c.Call(ctx, "PUT", path, body, nil)
	if isStatus(e, 404) {
		e = c.Call(ctx, "POST", strings.TrimSuffix(acl, "/"), []any{body}, nil)
	}
	if e != nil {
		return e
	}
	if exists {
		e = c.Call(ctx, "PUT", auth+"/users/"+url.PathEscape(id), map[string]any{"password": password, "is_superuser": false}, nil)
	} else {
		e = c.Call(ctx, "POST", auth+"/users", map[string]any{"user_id": id, "password": password, "is_superuser": false}, nil)
	}
	if e != nil {
		return e
	}
	return c.Kick(ctx, id)
}
func (c *Client) Revoke(ctx context.Context, id string) error {
	var first error
	for _, p := range []string{auth + "/users/" + url.PathEscape(id), acl + url.PathEscape(id), "/clients/" + url.PathEscape(id)} {
		e := c.Call(ctx, "DELETE", p, nil, nil)
		if e != nil && !isStatus(e, 404) && first == nil {
			first = e
		}
	}
	return first
}
