package builderapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JamesbbBriz/agent-workflow/canvas"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

const maxRequestBytes = 2 << 20

type Handler struct {
	core *workflow.AuthoringCore
	now  func() time.Time
}

func New(core *workflow.AuthoringCore, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	return &Handler{core: core, now: now}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/catalog":
		catalog, err := h.core.Catalog()
		h.write(w, catalog, err)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/v1/workflows/lint":
		var definition contractsv1.WorkflowDefinition
		if err := decode(r, &definition); err != nil {
			h.writeError(w, err)
			return
		}
		h.write(w, h.core.Lint(definition), nil)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/v1/workflows/preview":
		var request struct {
			Actor    string                         `json:"actor"`
			Workflow contractsv1.WorkflowDefinition `json:"workflow"`
		}
		if err := decode(r, &request); err != nil {
			h.writeError(w, err)
			return
		}
		preview, lint, err := h.core.Preview(request.Workflow, request.Actor)
		if err != nil {
			h.write(w, struct {
				Lint contractsv1.WorkflowLintReport `json:"lint"`
			}{lint}, err)
			return
		}
		h.write(w, struct {
			Preview contractsv1.WorkflowAdmissionPreview `json:"preview"`
			Lint    contractsv1.WorkflowLintReport       `json:"lint"`
		}{preview, lint}, nil)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/v1/workflows/confirm":
		var request struct {
			Actor    string                               `json:"actor"`
			Preview  contractsv1.WorkflowAdmissionPreview `json:"preview"`
			Job      contractsv1.JobDefinition            `json:"job"`
			Campaign contractsv1.CampaignDefinition       `json:"campaign"`
		}
		if err := decode(r, &request); err != nil {
			h.writeError(w, err)
			return
		}
		admission, err := h.core.Confirm(request.Preview, request.Actor, h.now().UTC())
		if err != nil {
			h.write(w, nil, err)
			return
		}
		replay, err := h.core.AdmissionReplay(string(admission.Workflow.Id))
		if err != nil {
			h.write(w, nil, err)
			return
		}
		snapshot, err := canvas.ProjectWithAdmissions(request.Job, request.Campaign, []contractsv1.WorkflowDefinition{admission.Workflow}, []contractsv1.ReplayBundle{replay})
		h.write(w, struct {
			Admission contractsv1.WorkflowAdmission `json:"admission"`
			Canvas    contractsv1.CanvasSnapshot    `json:"canvas"`
		}{admission, snapshot}, err)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/workflows/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/workflows/"), "/")
		if len(parts) != 3 || parts[1] != "versions" {
			h.notFound(w)
			return
		}
		version, err := strconv.Atoi(parts[2])
		if err != nil {
			h.writeError(w, errors.New("workflow version is invalid"))
			return
		}
		admission, err := h.core.ReadWorkflow(parts[0], version)
		h.write(w, admission, err)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/v1/approvals/preview":
		var request struct {
			Actor  string                    `json:"actor"`
			Brief  contractsv1.ApprovalBrief `json:"brief"`
			Source contractsv1.ReplayBundle  `json:"source"`
		}
		if err := decode(r, &request); err != nil {
			h.writeError(w, err)
			return
		}
		preview, err := h.core.PreviewApproval(request.Brief, request.Actor, request.Source)
		h.write(w, preview, err)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/v1/approvals/confirm":
		var request struct {
			Actor    string                      `json:"actor"`
			OptionID string                      `json:"option_id"`
			Preview  contractsv1.ApprovalPreview `json:"preview"`
			Canvas   contractsv1.CanvasSnapshot  `json:"canvas"`
		}
		if err := decode(r, &request); err != nil {
			h.writeError(w, err)
			return
		}
		receipt, err := h.core.ConfirmApproval(request.Preview, request.Actor, request.OptionID, h.now().UTC())
		if err != nil {
			h.write(w, nil, err)
			return
		}
		replay, err := h.core.ApprovalReplay(string(request.Preview.Brief.Id))
		if err != nil {
			h.write(w, nil, err)
			return
		}
		next, err := canvas.ApplyApproval(request.Canvas, replay)
		h.write(w, struct {
			Receipt contractsv1.Receipt        `json:"receipt"`
			Canvas  contractsv1.CanvasSnapshot `json:"canvas"`
		}{receipt, next}, err)
		return
	default:
		h.notFound(w)
	}
}

func decode(r *http.Request, target any) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return errors.New("content type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request JSON is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func (h *Handler) write(w http.ResponseWriter, data any, err error) {
	status := http.StatusOK
	response := map[string]any{"ok": true, "data": data}
	if err != nil {
		status = http.StatusConflict
		response = map[string]any{"ok": false, "error": err.Error(), "code": "request_rejected"}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error(), "code": "invalid_request"})
}
func (h *Handler) notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "route not found", "code": "not_found"})
}
