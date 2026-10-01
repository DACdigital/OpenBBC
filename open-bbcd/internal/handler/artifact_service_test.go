package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// memRows is an in-memory SessionArtifactStore with the repository's
// semantics (dedup, cap, pending/consumed, per-session scope).
type memRows struct {
	mu          sync.Mutex
	rows        []*types.SessionArtifact
	seq         int
	commitErr   error // if set, CommitUpload returns it
	precheckErr error // if set, PrecheckUpload returns it
	commits     int
	lookupErr   error // if set, LookupSessionArtifact returns it
}

func (m *memRows) findPending(sid, store, uri string) *types.SessionArtifact {
	for _, r := range m.rows {
		if r.SessionID == sid && r.StoreID == store && r.URI == uri && r.Origin == types.ArtifactOriginUpload && r.MessageID == "" {
			return r
		}
	}
	return nil
}

func (m *memRows) countPending(sid string) int {
	n := 0
	for _, r := range m.rows {
		if r.SessionID == sid && r.Origin == types.ArtifactOriginUpload && r.MessageID == "" {
			n++
		}
	}
	return n
}

func (m *memRows) PrecheckUpload(ctx context.Context, sid, store, uri string, max int) (*types.SessionArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.precheckErr != nil {
		return nil, m.precheckErr
	}
	if r := m.findPending(sid, store, uri); r != nil {
		c := *r
		return &c, nil
	}
	if m.countPending(sid) >= max {
		return nil, types.ErrPendingArtifactCap
	}
	return nil, nil
}

func (m *memRows) CommitUpload(ctx context.Context, a types.SessionArtifact, max int) (*types.SessionArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits++
	if m.commitErr != nil {
		return nil, m.commitErr
	}
	if r := m.findPending(a.SessionID, a.StoreID, a.URI); r != nil {
		c := *r
		return &c, nil
	}
	if m.countPending(a.SessionID) >= max {
		return nil, types.ErrPendingArtifactCap
	}
	m.seq++
	a.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", m.seq)
	a.Origin = types.ArtifactOriginUpload
	m.rows = append(m.rows, &a)
	c := a
	return &c, nil
}

func (m *memRows) ListPendingArtifacts(ctx context.Context, sid string) ([]*types.SessionArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*types.SessionArtifact{}
	for _, r := range m.rows {
		if r.SessionID == sid && r.Origin == types.ArtifactOriginUpload && r.MessageID == "" {
			c := *r
			out = append(out, &c)
		}
	}
	return out, nil
}

func (m *memRows) DeletePendingArtifact(ctx context.Context, sid, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.rows {
		if r.ID == id && r.SessionID == sid {
			if r.MessageID != "" {
				return types.ErrArtifactConsumed
			}
			m.rows = append(m.rows[:i], m.rows[i+1:]...)
			return nil
		}
	}
	return types.ErrNotFound
}

func (m *memRows) LookupSessionArtifact(ctx context.Context, sid, store, uri string) (*types.SessionArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	for i := len(m.rows) - 1; i >= 0; i-- {
		r := m.rows[i]
		if r.SessionID == sid && r.StoreID == store && r.URI == uri {
			c := *r
			return &c, nil
		}
	}
	return nil, types.ErrNotFound
}

// consumeAll marks every pending row of sid as claimed (simulates a turn).
func (m *memRows) consumeAll(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.SessionID == sid && r.MessageID == "" {
			r.MessageID = "msg-1"
		}
	}
}

// boHarness wires an ArtifactHandler over memRows + a fake store for session testSID/"v1".
type boHarness struct {
	h     *ArtifactHandler
	rows  *memRows
	store *fakeArtifactStore
	sess  *fakeSessionStore
}

func newBOHarness(t *testing.T, maxPending int) *boHarness {
	t.Helper()
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL, signedURL: "https://signed.example/x"}
	rows := &memRows{}
	sess := &fakeSessionStore{sessions: map[string]*types.ChatSession{testSID: {ID: testSID, AgentVersionID: "v1"}}}
	return &boHarness{
		h:    NewArtifactHandler(sess, rows, buildRegistry(t, store), 1, maxPending, nil),
		rows: rows, store: store, sess: sess,
	}
}

func (b *boHarness) upload(t *testing.T, filename, ct string, data []byte) (*httptest.ResponseRecorder, PendingArtifact) {
	t.Helper()
	body, mct := newMultipartBody(t, filename, ct, data)
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", body)
	req.Header.Set("Content-Type", mct)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", testSID)
	rec := httptest.NewRecorder()
	b.h.HandleUpload(rec, req)
	var got PendingArtifact
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
	}
	return rec, got
}

func TestUpload_WritesPendingRowAndReturnsPendingObject(t *testing.T) {
	b := newBOHarness(t, 10)
	rec, got := b.upload(t, "hello.txt", "text/plain", []byte("hello world"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	// PR #53 fields unchanged in meaning + additive id/status.
	sum := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got.StoreID != "MAIN" || got.URI != "sha256/"+sum || got.Sha256 != sum || got.SizeBytes != 11 ||
		got.Filename != "hello.txt" || got.MIME != "text/plain" || got.Status != "pending" || got.ID == "" {
		t.Fatalf("response = %+v", got)
	}
	if list, _ := b.rows.ListPendingArtifacts(context.Background(), testSID); len(list) != 1 || list[0].ID != got.ID {
		t.Fatalf("rows = %+v", list)
	}
}

func TestUpload_IdenticalBytesTwice_SameRowOnePut(t *testing.T) {
	b := newBOHarness(t, 10)
	_, first := b.upload(t, "a.txt", "text/plain", []byte("same"))
	rec, second := b.upload(t, "renamed.txt", "text/plain", []byte("same"))
	if rec.Code != http.StatusCreated || second.ID != first.ID || second.Filename != "a.txt" {
		t.Fatalf("dedup: %d %+v", rec.Code, second)
	}
	if b.store.putCalls > 1 {
		t.Fatalf("Put called %d times", b.store.putCalls)
	}
	if n := len(b.rows.rows); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	// The dedup hit is settled at pre-check: no second Stat, no second commit.
	if b.store.statCalls != 1 || b.rows.commits != 1 {
		t.Fatalf("stat %d commits %d, want 1/1", b.store.statCalls, b.rows.commits)
	}
}

func TestUpload_PrecheckError500_NoStoreOrCommit(t *testing.T) {
	b := newBOHarness(t, 10)
	b.rows.precheckErr = errors.New("db down")
	rec, _ := b.upload(t, "a.txt", "text/plain", []byte("x"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if b.store.statCalls != 0 || b.store.putCalls != 0 || b.rows.commits != 0 {
		t.Fatalf("store/commit touched: stat %d put %d commits %d", b.store.statCalls, b.store.putCalls, b.rows.commits)
	}
}

func TestUpload_AtCap_RefusedAtPrecheckWithoutPut(t *testing.T) {
	b := newBOHarness(t, 1)
	b.upload(t, "a.txt", "text/plain", []byte("one"))
	puts := b.store.putCalls
	rec, _ := b.upload(t, "b.txt", "text/plain", []byte("two"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if b.store.putCalls != puts || b.rows.commits != 1 {
		t.Fatalf("cap refusal reached the store/commit: puts %d→%d commits %d", puts, b.store.putCalls, b.rows.commits)
	}
	// Dedup at cap still succeeds.
	if rec, _ := b.upload(t, "a.txt", "text/plain", []byte("one")); rec.Code != http.StatusCreated {
		t.Fatalf("dedup at cap: %d", rec.Code)
	}
	// After a turn consumes them, uploads succeed again.
	b.rows.consumeAll(testSID)
	if rec, _ := b.upload(t, "b.txt", "text/plain", []byte("two")); rec.Code != http.StatusCreated {
		t.Fatalf("after consume: %d", rec.Code)
	}
}

func TestUpload_ReuploadOfConsumedContent_NewRow(t *testing.T) {
	b := newBOHarness(t, 10)
	_, first := b.upload(t, "a.txt", "text/plain", []byte("x"))
	b.rows.consumeAll(testSID)
	_, second := b.upload(t, "a.txt", "text/plain", []byte("x"))
	if second.ID == "" || second.ID == first.ID {
		t.Fatalf("want a new pending row, got %+v (first %+v)", second, first)
	}
}

func TestUpload_StatErrorFallsThroughToPut(t *testing.T) {
	b := newBOHarness(t, 10)
	b.store.statErr = errors.New("stat boom")
	rec, _ := b.upload(t, "a.txt", "text/plain", []byte("x"))
	if rec.Code != http.StatusCreated || b.store.putCalls != 1 {
		t.Fatalf("status %d puts %d", rec.Code, b.store.putCalls)
	}
}

func TestUpload_PutFailure502_NoRow(t *testing.T) {
	b := newBOHarness(t, 10)
	b.store.putErr = errors.New("put boom")
	rec, _ := b.upload(t, "a.txt", "text/plain", []byte("x"))
	if rec.Code != http.StatusBadGateway || len(b.rows.rows) != 0 {
		t.Fatalf("status %d rows %d", rec.Code, len(b.rows.rows))
	}
}

func TestUpload_OverMaxSize_413NoStoreCall(t *testing.T) {
	b := newBOHarness(t, 10) // maxUploadMB = 1
	rec, _ := b.upload(t, "big.bin", "application/octet-stream", make([]byte, 2<<20))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", rec.Code)
	}
	if b.store.putCalls != 0 || b.store.statCalls != 0 || b.rows.commits != 0 {
		t.Fatalf("store/DB touched: put %d stat %d commits %d", b.store.putCalls, b.store.statCalls, b.rows.commits)
	}
}

func TestUpload_CommitErrorsMapToStatuses(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{types.ErrNotFound, http.StatusNotFound},
		{types.ErrSessionLocked, http.StatusConflict},
		{types.ErrPendingArtifactCap, http.StatusConflict},
	} {
		b := newBOHarness(t, 10)
		b.rows.commitErr = c.err
		if rec, _ := b.upload(t, "a.txt", "text/plain", []byte("x")); rec.Code != c.want {
			t.Errorf("%v: status %d, want %d", c.err, rec.Code, c.want)
		}
	}
}

func TestUpload_LockedSession409BeforeRead(t *testing.T) {
	b := newBOHarness(t, 10)
	now := timeNow()
	b.sess.sessions[testSID].LockedAt = &now
	if rec, _ := b.upload(t, "a.txt", "text/plain", []byte("x")); rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
	if b.store.statCalls != 0 {
		t.Fatal("store touched for a locked session")
	}
}

func boRequest(method, path, versionID, sessionID string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.SetPathValue("version_id", versionID)
	req.SetPathValue("session_id", sessionID)
	return req
}

func TestListPending_OrderAndEmpty(t *testing.T) {
	b := newBOHarness(t, 10)
	rec := httptest.NewRecorder()
	b.h.HandleListPending(rec, boRequest(http.MethodGet, "/", "v1", testSID))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"pending_artifacts":[]}` {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body.String())
	}
	_, a := b.upload(t, "a.txt", "text/plain", []byte("a"))
	_, c := b.upload(t, "c.txt", "text/plain", []byte("c"))
	rec = httptest.NewRecorder()
	b.h.HandleListPending(rec, boRequest(http.MethodGet, "/", "v1", testSID))
	var got struct {
		PendingArtifacts []PendingArtifact `json:"pending_artifacts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.PendingArtifacts) != 2 || got.PendingArtifacts[0].ID != a.ID || got.PendingArtifacts[1].ID != c.ID || got.PendingArtifacts[0].Status != "pending" {
		t.Fatalf("list = %+v", got)
	}
	b.rows.consumeAll(testSID)
	rec = httptest.NewRecorder()
	b.h.HandleListPending(rec, boRequest(http.MethodGet, "/", "v1", testSID))
	if strings.TrimSpace(rec.Body.String()) != `{"pending_artifacts":[]}` {
		t.Fatalf("after consume: %s", rec.Body.String())
	}
}

func TestDeletePending_Statuses(t *testing.T) {
	b := newBOHarness(t, 1)
	_, a := b.upload(t, "a.txt", "text/plain", []byte("a"))
	del := func(id string) int {
		req := boRequest(http.MethodDelete, "/", "v1", testSID)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		b.h.HandleDeletePending(rec, req)
		return rec.Code
	}
	if code := del("00000000-0000-0000-0000-999999999999"); code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", code)
	}
	if code := del(a.ID); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code := del(a.ID); code != http.StatusNotFound {
		t.Fatalf("repeat delete: %d", code)
	}
	// The removed row no longer counts toward the cap (max 1).
	_, b2 := b.upload(t, "b.txt", "text/plain", []byte("b"))
	if b2.ID == "" {
		t.Fatal("upload after delete refused")
	}
	b.rows.consumeAll(testSID)
	if code := del(b2.ID); code != http.StatusConflict {
		t.Fatalf("delete consumed: %d", code)
	}
	// Locked BO session → 409.
	now := timeNow()
	b.sess.sessions[testSID].LockedAt = &now
	if code := del(b2.ID); code != http.StatusConflict {
		t.Fatalf("delete on locked session: %d", code)
	}
	// Removing a pending artifact drops the row only; blobs are
	// content-addressed and may back other rows.
	if b.store.deleteCalls != 0 {
		t.Fatalf("store Delete called %d times, want 0", b.store.deleteCalls)
	}
}

func TestBOArtifactRoutes_SessionOfOtherVersion(t *testing.T) {
	b := newBOHarness(t, 10)
	rec := httptest.NewRecorder()
	b.h.HandleListPending(rec, boRequest(http.MethodGet, "/", "v-other", testSID))
	// GetSession reports ErrSessionAgentMismatch → 403 (the BO mapping).
	if rec.Code != http.StatusForbidden {
		t.Fatalf("list on another version's session: %d, want 403", rec.Code)
	}
}

// postUpload sends body with the given Content-Type to the BO upload route.
func (b *boHarness) postUpload(body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", body)
	req.Header.Set("Content-Type", contentType)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", testSID)
	rec := httptest.NewRecorder()
	b.h.HandleUpload(rec, req)
	return rec
}

func TestUpload_NonFileFieldBeforeFile_Uploads(t *testing.T) {
	b := newBOHarness(t, 10)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("note", "hi"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreatePart(multipartHeader("a.txt", "text/plain"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("hello"))
	_ = mw.Close()
	rec := b.postUpload(&buf, mw.FormDataContentType())
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got PendingArtifact
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Filename != "a.txt" || got.SizeBytes != 5 || got.MIME != "text/plain" {
		t.Fatalf("response = %+v", got)
	}
}

func TestUpload_OnlyNonFileField_400MissingFile(t *testing.T) {
	b := newBOHarness(t, 10)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("note", "hi")
	_ = mw.Close()
	rec := b.postUpload(&buf, mw.FormDataContentType())
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing 'file' field") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if b.store.statCalls != 0 || b.store.putCalls != 0 || b.rows.commits != 0 {
		t.Fatal("store/DB touched")
	}
}

// A "file" part with no filename is a form value, not the upload (as with
// FormFile): alone it is 400; a later "file" part with a filename uploads.
func TestUpload_FileFieldWithoutFilename(t *testing.T) {
	b := newBOHarness(t, 10)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("file", "not a file")
	_ = mw.Close()
	if rec := b.postUpload(&buf, mw.FormDataContentType()); rec.Code != http.StatusBadRequest {
		t.Fatalf("value-only: status %d: %s", rec.Code, rec.Body.String())
	}

	buf.Reset()
	mw = multipart.NewWriter(&buf)
	_ = mw.WriteField("file", "not a file")
	fw, err := mw.CreatePart(multipartHeader("a.txt", "text/plain"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("hello"))
	_ = mw.Close()
	if rec := b.postUpload(&buf, mw.FormDataContentType()); rec.Code != http.StatusCreated {
		t.Fatalf("value then file: status %d: %s", rec.Code, rec.Body.String())
	}
}

// A body cut off inside the file part is malformed (400), as it was when
// ParseMultipartForm read it, not a 500 from the hash step.
func TestUpload_TruncatedFilePart_400(t *testing.T) {
	b := newBOHarness(t, 10)
	body, mct := newMultipartBody(t, "a.txt", "text/plain", []byte(strings.Repeat("x", 4096)))
	cut := body.Bytes()[:body.Len()/2]
	rec := b.postUpload(bytes.NewReader(cut), mct)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if b.store.statCalls != 0 || b.store.putCalls != 0 || b.rows.commits != 0 {
		t.Fatal("store/DB touched")
	}
}

func TestUpload_NotMultipart_400(t *testing.T) {
	b := newBOHarness(t, 10)
	rec := b.postUpload(strings.NewReader("hello"), "text/plain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if b.store.statCalls != 0 || b.store.putCalls != 0 || b.rows.commits != 0 {
		t.Fatal("store/DB touched")
	}
}

// raceAllocFactor is 2 under -race (race_enabled_test.go), else 1.
var raceAllocFactor = 1.0

// The upload keeps one copy of the file in memory. The old ParseMultipartForm
// + hashAndBuffer path allocated about 5.3x the file size for 6 MiB; streaming
// the part allocates about 2.7x, all of it hashAndBuffer's doubling growth
// (1+2+4+8 MiB). The bound sits between the two. Under -race every figure
// doubles (10.7x vs 5.3x), so the bound scales by raceAllocFactor.
func TestUpload_SingleBuffer_AllocationBound(t *testing.T) {
	const size = 6 << 20
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL, statHit: true}
	sess := &fakeSessionStore{sessions: map[string]*types.ChatSession{testSID: {ID: testSID, AgentVersionID: "v1"}}}
	rows := &memRows{}
	b := &boHarness{
		h:    NewArtifactHandler(sess, rows, buildRegistry(t, store), 8, 10, nil),
		rows: rows, store: store, sess: sess,
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 7)
	}
	body, mct := newMultipartBody(t, "big.bin", "application/octet-stream", data)
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/v1/chat/s1/artifacts", body)
	req.Header.Set("Content-Type", mct)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", testSID)
	rec := httptest.NewRecorder()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	b.h.HandleUpload(rec, req)
	runtime.ReadMemStats(&after)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	ratio := float64(after.TotalAlloc-before.TotalAlloc) / size
	t.Logf("allocated %.2fx the file size", ratio)
	if limit := 3 * raceAllocFactor; ratio >= limit {
		t.Fatalf("allocated %.2fx the file size, want < %.0fx", ratio, limit)
	}
}
