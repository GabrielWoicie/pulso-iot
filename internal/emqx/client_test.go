package emqx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestProvisionRevokeContract(t *testing.T) {
	var mu sync.Mutex
	exists := false
	rules := false
	failDelete := false
	calls := []string{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		u, p, ok := r.BasicAuth()
		if !ok || u != "key" || p != "secret" {
			t.Error("auth")
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		if strings.Contains(r.URL.Path, "/rules/clients/") && r.Method == "PUT" {
			var b struct {
				ID    string           `json:"clientid"`
				Rules []map[string]any `json:"rules"`
			}
			if e := json.NewDecoder(r.Body).Decode(&b); e != nil || b.ID != "dev-001" || len(b.Rules) != 3 {
				t.Error("rules schema")
			}
			for _, v := range b.Rules {
				if v["retain"] != false || !strings.HasPrefix(v["topic"].(string), "eq desbravatech/v1/devices/dev-001/") {
					t.Error(v)
				}
			}
			rules = true
			w.WriteHeader(204)
			return
		}
		if r.Method == "GET" {
			if !exists {
				w.WriteHeader(404)
			} else {
				w.Write([]byte(`{}`))
			}
			return
		}
		if r.Method == "POST" {
			if !rules {
				t.Error("user before ACL")
			}
			exists = true
			w.WriteHeader(201)
			return
		}
		if r.Method == "DELETE" && failDelete && strings.Contains(r.URL.Path, "/users/") {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer s.Close()
	c := New(s.URL, "key", "secret")
	ctx := context.Background()
	if e := c.Provision(ctx, "dev-001", "private-value", false); e != nil {
		t.Fatal(e)
	}
	if e := c.Provision(ctx, "dev-001", "other", false); e == nil {
		t.Fatal("overwrote existing")
	}
	if e := c.Provision(ctx, "dev-001", "rotated", true); e != nil {
		t.Fatal(e)
	}
	failDelete = true
	if e := c.Revoke(ctx, "dev-001"); e == nil {
		t.Fatal("partial revoke reported success")
	}
	if !strings.Contains(calls[len(calls)-1], "/clients/dev-001") {
		t.Fatal("did not kick after failed revoke")
	}
}
func TestPreflight(t *testing.T) {
	for _, bad := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var v any
			switch r.URL.Path {
			case "/api/v5/nodes":
				v = []map[string]string{{"version": "5.8.9"}}
			case "/api/v5/authentication/password_based:built_in_database":
				v = map[string]any{"user_id_type": "clientid", "enable": true}
			case "/api/v5/authorization/settings":
				no := "deny"
				if bad {
					no = "allow"
				}
				v = map[string]string{"no_match": no}
			case "/api/v5/authorization/sources":
				v = map[string]any{"sources": []map[string]any{{"type": "built_in_database", "enable": true}}}
			default:
				t.Error(r.URL.Path)
			}
			json.NewEncoder(w).Encode(v)
		}))
		e := New(s.URL, "k", "s").Preflight(context.Background())
		s.Close()
		if (e != nil) != bad {
			t.Fatal(e)
		}
	}
}
func TestNoBrokerSecretInErrors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"password":"must-not-leak"}`))
	}))
	defer s.Close()
	e := New(s.URL, "k", "s").Provision(context.Background(), "dev-001", "must-not-leak", false)
	if e == nil || strings.Contains(e.Error(), "must-not-leak") {
		t.Fatal(e)
	}
}
