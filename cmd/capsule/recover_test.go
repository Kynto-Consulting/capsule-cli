package main

import (
	"net/http"
	"strings"
	"testing"
)

const rcRun = `{"id":"` + uuidA + `","mode":"apply","status":"success","steps":[{"name":"F0 preflight","phase":"F0","status":"ok","detail":"ok"}],"report_url":"drills/x.json"}`

func TestRecoverPlanPrintsStepsAndRisks(t *testing.T) {
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/git/admin/recover": respond(202, `{"id":"`+uuidA+`","mode":"plan","status":"planned","plan_id":"abcdef1234567890","ready":true,"estimate_seconds":120,
			"steps":[{"name":"F0 preflight","phase":"F0","status":"pending"}],"risks":[{"code":"STALE_BACKUP","severity":"warning","subject":"x","message":"old"}]}`),
	})
	var err error
	out := capture(t, func() { err = run(t, mustCmd(t, "recover plan"), nil, f2("all", "true", "target", "ephemeral", "scope", "git,databases")) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "F0") || !strings.Contains(out, "STALE_BACKUP") {
		t.Fatalf("plan output: %s", out)
	}
	req := f.find("POST", "/git/admin/recover")
	if req.Body["mode"] != "plan" || req.Body["target"] != "ephemeral" || req.Header.Get("Idempotency-Key") == "" {
		t.Fatalf("request: %+v %v", req.Body, req.Header)
	}
}

func TestRecoverPlanBlockedExitsWithBlockedCode(t *testing.T) {
	newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST " + projURL() + "/recover": respond(202, `{"id":"`+uuidA+`","mode":"plan","status":"planned","ready":false,"steps":[],"risks":[{"code":"NO_DB_BACKUP","severity":"error","subject":"p","message":"m"}]}`),
	})
	var err error
	capture(t, func() { err = run(t, mustCmd(t, "recover plan"), nil, nil) })
	if exitCodeFor(err) != exitBlocked {
		t.Fatalf("want exit %d, got %v", exitBlocked, err)
	}
}

func TestRecoverApplyNeedsConfirmationAndSendsFlags(t *testing.T) {
	f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST " + projURL() + "/recover": respond(202, rcRun),
	})
	var err error
	capture(t, func() { err = run(t, mustCmd(t, "recover apply"), nil, nil) })
	if exitCodeFor(err) != exitUsage || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without --yes: %v", err)
	}
	if f.count("POST", "/recover") != 0 {
		t.Fatal("nothing may be sent without confirmation")
	}
	capture(t, func() { err = run(t, mustCmd(t, "recover apply"), nil, f2("yes", "true", "force-overwrite", "true", "snapshot", uuidB)) })
	if err != nil {
		t.Fatal(err)
	}
	req := f.find("POST", "/recover")
	if req.Body["mode"] != "apply" || req.Body["force_overwrite"] != true || req.Body["snapshot_id"] != uuidB {
		t.Fatalf("body: %+v", req.Body)
	}
}

func TestRecoverVerifyAndStatus(t *testing.T) {
	newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/git/admin/recover":          respond(202, rcRun),
		"GET /api/v1/git/admin/recover/" + uuidA: respond(200, rcRun),
	})
	var err error
	out := capture(t, func() { err = run(t, mustCmd(t, "recover verify"), nil, f2("all", "true")) })
	if err != nil || !strings.Contains(out, "status: success") {
		t.Fatalf("%v %s", err, out)
	}
	out = capture(t, func() { err = run(t, mustCmd(t, "recover status"), []string{uuidA}, nil) })
	if err != nil || !strings.Contains(out, "drills/x.json") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestRecoverRejectsBadFlags(t *testing.T) {
	newFake(t, nil)
	var err error
	capture(t, func() { err = run(t, mustCmd(t, "recover plan"), nil, f2("all", "true", "at", "yesterday")) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("bad --at: %v", err)
	}
	capture(t, func() { err = run(t, mustCmd(t, "recover plan"), nil, f2("all", "true", "target", "moon")) })
	if exitCodeFor(err) != exitUsage {
		t.Fatalf("bad --target: %v", err)
	}
}
