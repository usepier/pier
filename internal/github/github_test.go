package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func server(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body map[string]any)) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Header().Set("X-RateLimit-Remaining", "4321")
		h(w, r, body)
	}))
	t.Cleanup(s.Close)
	return &Client{API: s.URL, Token: func(string) string { return "tok" }}
}

const prJSON = `{"id":"PR_1","number":12,"title":"Fix login","url":"https://github.com/acme/shop/pull/12","state":"OPEN","isDraft":false,"merged":false,
"mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED","reviewDecision":"CHANGES_REQUESTED","headRefOid":"abc","headRefName":"fix-login","baseRefName":"main",
"additions":10,"deletions":2,"updatedAt":"2026-10-08T10:00:00Z","headRepositoryOwner":{"login":"acme"},
"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
 {"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"u1","databaseId":77,"checkSuite":{"workflowRun":{"databaseId":9}}},
 {"__typename":"CheckRun","name":"lint","status":"IN_PROGRESS","conclusion":null,"detailsUrl":"u2","databaseId":78,"checkSuite":{"workflowRun":{"databaseId":9}}},
 {"__typename":"StatusContext","context":"deploy/preview","state":"SUCCESS","targetUrl":"u3"}
]}}}}]}}`

func TestForBranches(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		q := body["query"].(string)
		if !strings.Contains(q, "b0: pullRequests(headRefName: $b0") || body["variables"].(map[string]any)["b1"] != "nope" {
			t.Errorf("query = %s %v", q, body["variables"])
		}
		fork := strings.Replace(prJSON, `"login":"acme"`, `"login":"someone"`, 1)
		io.WriteString(w, `{"data":{"repository":{"b0":{"nodes":[`+fork+`,`+prJSON+`]},"b1":{"nodes":[]}}}}`)
	})
	prs, err := c.ForBranches(context.Background(), Repo{"github.com", "acme", "shop"}, []string{"fix-login", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := prs["fix-login"]
	if !ok || len(prs) != 1 {
		t.Fatalf("prs = %+v", prs)
	}
	if p.Number != 12 || p.State != "open" || p.Review != "changes_requested" || p.Mergeable != "blocked" || p.HeadOid != "abc" {
		t.Errorf("pr = %+v", p)
	}
	if p.ChecksState() != "failing" || p.Checks[0].JobID != 77 || p.Checks[0].RunID != 9 || p.Checks[1].State != "pending" || p.Checks[2].State != "passing" {
		t.Errorf("checks = %+v", p.Checks)
	}
	if c.Limit().Remaining != 4321 {
		t.Errorf("rate limit = %+v", c.Limit())
	}
}

func TestMergeGuardsTheHead(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		v := body["variables"].(map[string]any)
		if v["head"] != "abc" || v["m"] != "SQUASH" {
			t.Errorf("vars = %v", v)
		}
		io.WriteString(w, `{"data":{"mergePullRequest":null},"errors":[{"type":"UNPROCESSABLE","message":"Head branch was modified. Review and try the merge again."}]}`)
	})
	err := c.Merge(context.Background(), Repo{"github.com", "acme", "shop"}, "PR_1", "squash", "abc")
	if !errors.Is(err, ErrHeadMoved) {
		t.Errorf("merge = %v", err)
	}
	if err := c.Merge(context.Background(), Repo{"github.com", "acme", "shop"}, "PR_1", "squash", ""); err == nil {
		t.Error("a merge without the reviewed head went ahead")
	}
}

func TestJobLogKeepsTheEnd(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if !strings.HasSuffix(r.URL.Path, "/repos/acme/shop/actions/jobs/77/logs") {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, strings.Repeat("noise\n", 100)+"FAIL TestLogin\n")
	})
	log, err := c.JobLog(context.Background(), Repo{"github.com", "acme", "shop"}, 77, 40)
	if err != nil || !strings.HasSuffix(log, "FAIL TestLogin\n") || len(log) > 40 || strings.HasPrefix(log, "oise") {
		t.Errorf("log = %q %v", log, err)
	}
}

func TestNoLogin(t *testing.T) {
	c := &Client{API: "http://127.0.0.1:1", Token: func(string) string { return "" }}
	if _, err := c.ForBranches(context.Background(), Repo{"github.com", "a", "b"}, []string{"x"}); !errors.Is(err, ErrNoToken) {
		t.Errorf("err = %v", err)
	}
}

func TestParseOrigin(t *testing.T) {
	if r, ok := ParseOrigin("github.com/acme/shop"); !ok || r.Owner != "acme" || r.Name != "shop" {
		t.Errorf("parse = %+v", r)
	}
	if _, ok := ParseOrigin("name:shop"); ok {
		t.Error("a name-only origin parsed")
	}
}
