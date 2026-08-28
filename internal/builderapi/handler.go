package builderapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JamesbbBriz/agent-workflow/canvas"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
	"github.com/JamesbbBriz/agent-workflow/workflow"
)

const maxRequestBytes = 2 << 20
const localApprovalActor = "local-operator"

type Handler struct {
	core      *workflow.AuthoringCore
	now       func() time.Time
	mu        sync.Mutex
	canvas    *contractsv1.CanvasSnapshot
	portfolio *contractsv1.CanvasPortfolioSnapshot
	jobs      map[contractsv1.Identifier]contractsv1.JobDefinition
	campaigns map[contractsv1.Identifier]contractsv1.CampaignDefinition
	webMCP    *webMCPGate
}

type DefinitionHistory struct {
	Jobs      []contractsv1.JobDefinition
	Campaigns []contractsv1.CampaignDefinition
}

func New(core *workflow.AuthoringCore, now func() time.Time) http.Handler {
	return NewWithCanvas(core, now, nil)
}

func NewWithCanvas(core *workflow.AuthoringCore, now func() time.Time, snapshot *contractsv1.CanvasSnapshot) http.Handler {
	var portfolio *contractsv1.CanvasPortfolioSnapshot
	if snapshot != nil {
		projected, err := canvas.ProjectPortfolio(snapshot.Definition.Job, []contractsv1.CanvasSnapshot{*snapshot}, snapshot.Definition.Campaign.Id)
		if err == nil {
			portfolio = &projected
		}
	}
	return NewWithPortfolio(core, now, portfolio)
}

func NewWithPortfolio(core *workflow.AuthoringCore, now func() time.Time, portfolio *contractsv1.CanvasPortfolioSnapshot) http.Handler {
	return NewWithPortfolioHistory(core, now, portfolio, DefinitionHistory{})
}

func NewWithPortfolioHistory(core *workflow.AuthoringCore, now func() time.Time, portfolio *contractsv1.CanvasPortfolioSnapshot, history DefinitionHistory) http.Handler {
	if now == nil {
		now = time.Now
	}
	handler := &Handler{core: core, now: now, portfolio: portfolio, jobs: map[contractsv1.Identifier]contractsv1.JobDefinition{}, campaigns: map[contractsv1.Identifier]contractsv1.CampaignDefinition{}}
	for _, job := range history.Jobs {
		handler.jobs[job.Id] = job
	}
	for _, campaign := range history.Campaigns {
		handler.campaigns[campaign.Id] = campaign
	}
	if portfolio != nil {
		handler.jobs[portfolio.Job.Id] = portfolio.Job
		for index := range portfolio.Campaigns {
			handler.campaigns[portfolio.Campaigns[index].Canvas.Definition.Campaign.Id] = portfolio.Campaigns[index].Canvas.Definition.Campaign
			if portfolio.Campaigns[index].CampaignId == portfolio.SelectedCampaignId {
				handler.canvas = &portfolio.Campaigns[index].Canvas
				break
			}
		}
	}
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.webMCP != nil && r.Method == http.MethodGet && r.URL.Path == "/v1/webmcp/session" {
		h.serveWebMCPSession(w, r)
		return
	}
	if h.webMCP != nil && r.Header.Get(webMCPToolHeader) != "" {
		h.serveWebMCP(w, r)
		return
	}
	h.serve(w, r)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v2/canvas":
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.portfolio == nil {
			h.write(w, nil, errors.New("trusted Canvas portfolio is unavailable"))
			return
		}
		h.write(w, *h.portfolio, nil)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/v1/canvas":
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.canvas == nil {
			h.write(w, nil, errors.New("trusted Canvas is unavailable"))
			return
		}
		h.write(w, *h.canvas, nil)
		return
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
			Job      contractsv1.JobDefinition      `json:"job"`
			Campaign contractsv1.CampaignDefinition `json:"campaign"`
			Workflow contractsv1.WorkflowDefinition `json:"workflow"`
		}
		if err := decode(r, &request); err != nil {
			h.writeError(w, err)
			return
		}
		preview, lint, err := h.core.Preview(request.Job, request.Campaign, request.Workflow, request.Actor)
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
			Actor   string                               `json:"actor"`
			Preview contractsv1.WorkflowAdmissionPreview `json:"preview"`
		}
		if err := decode(r, &request); err != nil {
			h.writeError(w, err)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if job, ok := h.jobs[request.Preview.Job.Id]; ok && !reflect.DeepEqual(job, request.Preview.Job) {
			h.write(w, nil, errors.New("Job definition is immutable after its first Campaign admission"))
			return
		}
		if campaign, ok := h.campaigns[request.Preview.Campaign.Id]; ok && !reflect.DeepEqual(campaign, request.Preview.Campaign) {
			h.write(w, nil, errors.New("Campaign definition is immutable after its first Workflow admission"))
			return
		}
		definitions, err := h.admissionDefinitions(request.Preview)
		if err != nil {
			h.write(w, nil, err)
			return
		}
		admission, err := h.core.ConfirmWithAudit(request.Preview, request.Actor, h.now().UTC(), webMCPAdmissionAudit(r.Context()))
		if err != nil {
			h.write(w, nil, err)
			return
		}
		replay, err := h.core.AdmissionReplay(string(admission.Workflow.Id))
		if err != nil {
			h.write(w, nil, err)
			return
		}
		snapshot, err := canvas.ProjectWithAdmissions(admission.Job, admission.Campaign, definitions, []contractsv1.ReplayBundle{replay})
		if err == nil {
			err = h.mergeCampaignCanvas(snapshot)
			if err == nil {
				h.jobs[admission.Job.Id] = admission.Job
				h.campaigns[admission.Campaign.Id] = admission.Campaign
				snapshot = *h.canvas
			}
		}
		h.write(w, struct {
			Admission contractsv1.WorkflowAdmission        `json:"admission"`
			Canvas    contractsv1.CanvasSnapshot           `json:"canvas"`
			Portfolio *contractsv1.CanvasPortfolioSnapshot `json:"portfolio,omitempty"`
		}{admission, snapshot, h.portfolio}, err)
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
			Brief             contractsv1.ApprovalBrief `json:"brief"`
			SourceAggregateID string                    `json:"source_aggregate_id"`
		}
		if err := decode(r, &request); err != nil {
			h.writeError(w, err)
			return
		}
		preview, err := h.core.PreviewApproval(request.Brief, localApprovalActor, request.SourceAggregateID)
		h.write(w, preview, err)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/v1/approvals/confirm":
		var request struct {
			OptionID string                      `json:"option_id"`
			Preview  contractsv1.ApprovalPreview `json:"preview"`
		}
		if err := decode(r, &request); err != nil {
			h.writeError(w, err)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		target, err := h.campaignCanvas(request.Preview.Brief.Action.CampaignId)
		if err != nil {
			h.write(w, nil, errors.New("trusted Canvas is unavailable"))
			return
		}
		if replay, err := h.core.ApprovalReplay(string(request.Preview.Brief.Id)); err == nil {
			receipt, err := h.core.ConfirmApproval(request.Preview, localApprovalActor, request.OptionID, h.now().UTC())
			if err != nil {
				h.write(w, nil, err)
				return
			}
			h.writeApproval(w, target, receipt, replay)
			return
		}
		if err := canvas.ValidateApprovalTarget(target, request.Preview.Brief); err != nil {
			h.write(w, nil, err)
			return
		}
		receipt, err := h.core.ConfirmApproval(request.Preview, localApprovalActor, request.OptionID, h.now().UTC())
		if err != nil {
			h.write(w, nil, err)
			return
		}
		replay, err := h.core.ApprovalReplay(string(request.Preview.Brief.Id))
		if err != nil {
			h.write(w, nil, err)
			return
		}
		h.writeApproval(w, target, receipt, replay)
		return
	default:
		h.notFound(w)
	}
}

func (h *Handler) writeApproval(w http.ResponseWriter, target contractsv1.CanvasSnapshot, receipt contractsv1.Receipt, replay contractsv1.ReplayBundle) {
	for _, projected := range target.ApprovalReplays {
		if projected.BundleHash == replay.BundleHash {
			h.write(w, struct {
				Receipt contractsv1.Receipt        `json:"receipt"`
				Canvas  contractsv1.CanvasSnapshot `json:"canvas"`
			}{receipt, target}, nil)
			return
		}
	}
	next, err := canvas.ApplyApproval(target, replay)
	if err == nil {
		err = h.replaceCampaignCanvas(next)
	}
	h.write(w, struct {
		Receipt contractsv1.Receipt        `json:"receipt"`
		Canvas  contractsv1.CanvasSnapshot `json:"canvas"`
	}{receipt, next}, err)
}

func (h *Handler) campaignCanvas(campaignID contractsv1.Identifier) (contractsv1.CanvasSnapshot, error) {
	if h.portfolio != nil {
		for _, item := range h.portfolio.Campaigns {
			if item.CampaignId == campaignID {
				return item.Canvas, nil
			}
		}
	}
	if h.canvas != nil && h.canvas.Definition.Campaign.Id == campaignID {
		return *h.canvas, nil
	}
	return contractsv1.CanvasSnapshot{}, errors.New("Campaign Canvas is unavailable")
}

func (h *Handler) mergeCampaignCanvas(next contractsv1.CanvasSnapshot) error {
	if h.portfolio != nil && reflect.DeepEqual(h.portfolio.Job, next.Definition.Job) {
		for _, current := range h.portfolio.Campaigns {
			if current.CampaignId == next.Definition.Campaign.Id {
				next = canvas.MergeAdmissionReadback(current.Canvas, next)
				break
			}
		}
	}
	return h.replaceCampaignCanvas(next)
}

func (h *Handler) replaceCampaignCanvas(next contractsv1.CanvasSnapshot) error {
	campaigns := []contractsv1.CanvasSnapshot{next}
	if h.portfolio != nil && reflect.DeepEqual(h.portfolio.Job, next.Definition.Job) {
		campaigns = make([]contractsv1.CanvasSnapshot, 0, len(h.portfolio.Campaigns)+1)
		for _, item := range h.portfolio.Campaigns {
			campaigns = append(campaigns, item.Canvas)
		}
		replaced := false
		for index := range campaigns {
			if campaigns[index].Definition.Campaign.Id == next.Definition.Campaign.Id {
				campaigns[index] = next
				replaced = true
				break
			}
		}
		if !replaced {
			campaigns = append(campaigns, next)
		}
	}
	portfolio, err := canvas.ProjectPortfolio(next.Definition.Job, campaigns, next.Definition.Campaign.Id)
	if err != nil {
		return err
	}
	h.canvas, h.portfolio = &next, &portfolio
	return nil
}

func (h *Handler) admissionDefinitions(preview contractsv1.WorkflowAdmissionPreview) ([]contractsv1.WorkflowDefinition, error) {
	definitions := []contractsv1.WorkflowDefinition{preview.Workflow}
	if h.canvas != nil && h.canvas.Definition.Job.Id == preview.Job.Id && h.canvas.Definition.Campaign.Id == preview.Campaign.Id {
		definitions = append([]contractsv1.WorkflowDefinition(nil), h.canvas.Definition.Workflows...)
		replaced := false
		for index := range definitions {
			if definitions[index].Id == preview.Workflow.Id {
				definitions[index] = preview.Workflow
				replaced = true
			}
		}
		if !replaced {
			definitions = append(definitions, preview.Workflow)
		}
	}
	planned := make(map[contractsv1.WorkflowRef]bool, len(preview.Campaign.WorkflowPlan))
	for _, ref := range preview.Campaign.WorkflowPlan {
		planned[ref] = true
	}
	filtered := definitions[:0]
	for _, definition := range definitions {
		ref := contractsv1.WorkflowRef(fmt.Sprintf("%s@%d", definition.Id, definition.Version))
		if planned[ref] {
			filtered = append(filtered, definition)
		}
	}
	definitions = filtered
	if _, err := canvas.Project(preview.Job, preview.Campaign, definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

func decode(r *http.Request, target any) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return errors.New("content type must be application/json")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		return errors.New("request body exceeds 2 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
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
