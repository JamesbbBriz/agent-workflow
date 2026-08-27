package builderapi

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestWebMCPRateLimitAuditIsAtomicAcrossConcurrentWindowRollover(t *testing.T) {
	audit := &blockingAudit{blocked: make(chan struct{}), release: make(chan struct{})}
	gate := &webMCPGate{audit: audit, limit: 1, usage: map[string]webMCPUsage{}}
	minute := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	record := webMCPAuditRecord{SchemaVersion: 1, RequestID: "123e4567-e89b-42d3-a456-426614174000", Subject: webMCPLocalSubject, PageOrigin: "http://127.0.0.1:5173", Tool: "inspect_current_canvas", InputsSHA256: digestBytes([]byte(`{}`))}
	if allowed, err := gate.admit(record, minute); err != nil || !allowed {
		t.Fatalf("initial admission = %v, %v", allowed, err)
	}
	denied := make(chan error, 1)
	go func() {
		allowed, err := gate.admit(record, minute)
		if allowed && err == nil {
			err = errUnexpectedAdmission
		}
		denied <- err
	}()
	<-audit.blocked
	next := make(chan error, 1)
	go func() {
		allowed, err := gate.admit(record, minute.Add(time.Minute))
		if !allowed && err == nil {
			err = errUnexpectedDenial
		}
		next <- err
	}()
	select {
	case <-next:
		t.Fatal("next-window admission bypassed the pending denial audit")
	case <-time.After(20 * time.Millisecond):
	}
	close(audit.release)
	if err := <-denied; err != nil {
		t.Fatal(err)
	}
	if err := <-next; err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(audit.Bytes(), []byte(`"outcome":"rate_limited"`)) {
		t.Fatal("concurrent denial audit was not persisted")
	}
}

var (
	errUnexpectedAdmission = errors.New("rate-limited request was admitted")
	errUnexpectedDenial    = errors.New("next-window request was denied")
)

type blockingAudit struct {
	bytes.Buffer
	blocked chan struct{}
	release chan struct{}
}

func (w *blockingAudit) Write(body []byte) (int, error) {
	close(w.blocked)
	<-w.release
	return w.Buffer.Write(body)
}
