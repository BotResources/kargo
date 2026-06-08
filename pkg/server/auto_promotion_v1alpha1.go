package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/pkg/api"
	libhttp "github.com/akuity/kargo/pkg/http"
	"github.com/akuity/kargo/pkg/logging"
	"github.com/akuity/kargo/pkg/server/user"
)

// autoPromotionCandidatesResponse lists the current auto-promotion candidate
// for each requested origin on a Stage.
type autoPromotionCandidatesResponse struct {
	Candidates []autoPromotionCandidate `json:"candidates"`
} // @name AutoPromotionCandidatesResponse

// autoPromotionCandidate identifies the Freight auto-promotion would currently
// choose for one origin.
type autoPromotionCandidate struct {
	Origin      kargoapi.FreightOrigin `json:"origin"`
	FreightName string                 `json:"freightName"`
} // @name AutoPromotionCandidate

// resumeStageAutoPromotionRequest identifies the held origin to resume.
type resumeStageAutoPromotionRequest struct {
	// Origin identifies the held Freight origin to resume.
	// +kubebuilder:validation:Required
	Origin *kargoapi.FreightOrigin `json:"origin"`
} // @name ResumeStageAutoPromotionRequest

// @id GetStageAutoPromotionCandidates
// @Summary Get Stage auto-promotion candidates
// @Description List the newest currently auto-promotable Freight for each
// @Description origin requested by the Stage.
// @Tags Core, Project-Level
// @Security BearerAuth
// @Produce json
// @Param project path string true "Project name"
// @Param stage path string true "Stage name"
// @Success 200 {object} autoPromotionCandidatesResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 504 {object} ErrorResponse
// @Router /v1beta1/projects/{project}/stages/{stage}/auto-promotion/candidates [get]
func (s *server) getStageAutoPromotionCandidates(c *gin.Context) {
	ctx := c.Request.Context()
	// Authorization is an implicit Stage read via the authorizing client in
	// getRESTStage. Candidate calculation then reads ProjectConfig and the
	// Stage's available Freight via the internal client (see
	// getAutoPromotionCandidates). This is intentional and mirrors the
	// getFreightLinks/getStageLinks pattern: a caller who can read a Stage is
	// entitled to see the names and origins of the Freight that Stage can
	// currently auto-promote (already visible via the Stage's own status), so we
	// do not require separate Freight/ProjectConfig read permissions here.
	stage, ok := s.getRESTStage(ctx, c.Param("project"), c.Param("stage"), c)
	if !ok {
		return
	}

	candidates, err := s.getAutoPromotionCandidates(ctx, stage)
	if err != nil {
		_ = c.Error(err)
		return
	}

	resp := autoPromotionCandidatesResponse{
		Candidates: make([]autoPromotionCandidate, 0, len(candidates)),
	}
	for _, freight := range candidates {
		resp.Candidates = append(resp.Candidates, autoPromotionCandidate{
			Origin:      freight.Origin,
			FreightName: freight.Name,
		})
	}
	slices.SortFunc(resp.Candidates, func(lhs, rhs autoPromotionCandidate) int {
		return strings.Compare(lhs.Origin.String(), rhs.Origin.String())
	})

	c.JSON(http.StatusOK, resp)
}

// @id ResumeStageAutoPromotion
// @Summary Resume Stage auto-promotion
// @Description Clear active auto-promotion holds for a Stage.
// @Tags Core, Project-Level
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param project path string true "Project name"
// @Param stage path string true "Stage name"
// @Param body body resumeStageAutoPromotionRequest true "Resume request"
// @Success 204
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 504 {object} ErrorResponse
// @Router /v1beta1/projects/{project}/stages/{stage}/auto-promotion/resume [post]
func (s *server) resumeStageAutoPromotion(c *gin.Context) {
	ctx := c.Request.Context()
	project := c.Param("project")
	stageName := c.Param("stage")
	key := client.ObjectKey{Namespace: project, Name: stageName}

	var req resumeStageAutoPromotionRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if !bindJSONOrError(c, &req) {
			return
		}
	}
	if req.Origin == nil || req.Origin.Kind == "" || req.Origin.Name == "" {
		_ = c.Error(libhttp.ErrorStr(
			"origin kind and name are required",
			http.StatusBadRequest,
		))
		return
	}
	if _, err := kargoapi.ParseFreightOriginKey(req.Origin.String()); err != nil {
		_ = c.Error(libhttp.ErrorStr(
			fmt.Sprintf("invalid origin: %s", err.Error()),
			http.StatusBadRequest,
		))
		return
	}

	if err := s.authorizeFn(
		ctx,
		"promote",
		kargoapi.GroupVersion.WithResource("stages"),
		"",
		key,
	); err != nil {
		_ = c.Error(err)
		return
	}

	// The custom "promote" verb was authorized above. The internal client lets
	// users who can promote, but cannot read Stages directly, resume automation.
	stage := &kargoapi.Stage{}
	if err := s.client.InternalClient().Get(ctx, key, stage); err != nil {
		if apierrors.IsNotFound(err) {
			_ = c.Error(libhttp.ErrorStr(
				fmt.Sprintf("Stage %q not found in project %q", stageName, project),
				http.StatusNotFound,
			))
			return
		}
		_ = c.Error(err)
		return
	}

	hold, ok := stage.Status.GetAutoPromotionHold(*req.Origin)
	if !ok {
		_ = c.Error(libhttp.ErrorStr(
			"Stage has no active auto-promotion hold for the requested origin",
			http.StatusNotFound,
		))
		return
	}
	if hold.State == kargoapi.AutoPromotionHoldStatePending {
		_ = c.Error(libhttp.ErrorStr(
			fmt.Sprintf(
				"auto-promotion cannot be resumed while pending hold Promotion %q is still settling",
				hold.PromotionName,
			),
			http.StatusConflict,
		))
		return
	}
	// State is a validated enum (Pending|Active), so reaching here means Active.
	// The patch below re-verifies identity against live state, turning any
	// concurrent change into a 409.
	expectedHold := hold

	changed, err := s.patchStageAutoPromotionHolds(ctx, key, func(status *kargoapi.StageStatus) bool {
		return removeAutoPromotionHolds(status, func(origin string, hold kargoapi.AutoPromotionHold) bool {
			return origin == req.Origin.String() &&
				hold.State == kargoapi.AutoPromotionHoldStateActive &&
				api.AutoPromotionHoldIdentityMatches(hold, expectedHold)
		})
	})
	if err != nil {
		_ = c.Error(fmt.Errorf("clear auto-promotion holds: %w", err))
		return
	}
	if !changed {
		_ = c.Error(libhttp.ErrorStr(
			"auto-promotion hold changed; reload and try again",
			http.StatusConflict,
		))
		return
	}
	// The caller was authorized for the custom "promote" verb above.
	// The internal client performs the mechanical refresh annotation write
	// because users are not generally allowed to patch Stages.
	if _, err = api.RefreshStage(ctx, s.client.InternalClient(), key); err != nil {
		logging.LoggerFromContext(ctx).Error(
			err,
			"error refreshing Stage after resuming auto-promotion",
			"stage", stageName,
		)
	}
	c.Status(http.StatusNoContent)
}

func (s *server) getRESTStage(
	ctx context.Context,
	project string,
	stageName string,
	c *gin.Context,
) (*kargoapi.Stage, bool) {
	stage := &kargoapi.Stage{}
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: project, Name: stageName}, stage); err != nil {
		if apierrors.IsNotFound(err) {
			_ = c.Error(libhttp.ErrorStr(
				fmt.Sprintf("Stage %q not found in project %q", stageName, project),
				http.StatusNotFound,
			))
			return nil, false
		}
		_ = c.Error(err)
		return nil, false
	}
	return stage, true
}

func (s *server) getAutoPromotionCandidate(
	ctx context.Context,
	stage *kargoapi.Stage,
	origin kargoapi.FreightOrigin,
) (*kargoapi.Freight, error) {
	candidates, err := s.getAutoPromotionCandidates(ctx, stage)
	if err != nil {
		return nil, err
	}
	candidate := candidates[origin.String()]
	if candidate == nil {
		return nil, nil
	}
	return candidate.DeepCopy(), nil
}

func (s *server) getAutoPromotionCandidates(
	ctx context.Context,
	stage *kargoapi.Stage,
) (map[string]*kargoapi.Freight, error) {
	if s.isAutoPromotionEnabledFn == nil && s.client == nil {
		return map[string]*kargoapi.Freight{}, nil
	}
	isAutoPromotionEnabledFn := s.isAutoPromotionEnabledFn
	if isAutoPromotionEnabledFn == nil {
		isAutoPromotionEnabledFn = api.IsAutoPromotionEnabled
	}
	autoPromotionClient := client.Client(s.client)
	if s.client != nil {
		// Candidate calculation is a mechanical Stage-level decision. Endpoint
		// handlers authorize the user before reaching this path, then use the
		// internal client so promote permissions do not also require unrelated
		// ProjectConfig, Warehouse, and Freight read permissions.
		autoPromotionClient = s.client.InternalClient()
	}
	enabled, err := isAutoPromotionEnabledFn(ctx, autoPromotionClient, stage.ObjectMeta)
	if err != nil {
		return nil, fmt.Errorf("check auto-promotion enablement: %w", err)
	}
	if !enabled {
		return map[string]*kargoapi.Freight{}, nil
	}

	getAvailableFreightForStageFn := s.getAutoPromotionAvailableFreightForStageFn
	if getAvailableFreightForStageFn == nil && s.client != nil {
		getAvailableFreightForStageFn = s.getAutoPromotionAvailableFreightForStage
	}
	if getAvailableFreightForStageFn == nil {
		return map[string]*kargoapi.Freight{}, nil
	}
	availableFreight, err := getAvailableFreightForStageFn(ctx, stage)
	if err != nil {
		return nil, fmt.Errorf("get available Freight for Stage: %w", err)
	}

	selected, err := api.SelectAutoPromotionCandidates(stage, availableFreight)
	if err != nil {
		return nil, fmt.Errorf("select auto-promotion candidates: %w", err)
	}
	candidates := make(map[string]*kargoapi.Freight)
	for origin, freight := range selected {
		candidates[origin] = freight.DeepCopy()
	}
	return candidates, nil
}

func (s *server) getAutoPromotionAvailableFreightForStage(
	ctx context.Context,
	stage *kargoapi.Stage,
) ([]kargoapi.Freight, error) {
	return api.ListFreightAvailableToStage(ctx, s.client.InternalClient(), stage)
}

// patchStageAutoPromotionHolds mutates Stage status with optimistic locking,
// re-running mutate against each fresh snapshot because the caller's
// preconditions must be re-checked after every conflict retry. The internal
// client performs the write because Stage status is controller/API-owned, not
// directly user-writable; callers authorize the request beforehand.
func (s *server) patchStageAutoPromotionHolds(
	ctx context.Context,
	key client.ObjectKey,
	mutate func(*kargoapi.StageStatus) bool,
) (bool, error) {
	var changed bool
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		stage := &kargoapi.Stage{}
		if err := s.client.InternalClient().Get(ctx, key, stage); err != nil {
			return err
		}
		original := stage.DeepCopy()
		if changed = mutate(&stage.Status); !changed {
			return nil
		}
		return s.client.InternalClient().Status().Patch(
			ctx,
			stage,
			client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
		)
	})
	return changed, err
}

func removeAutoPromotionHolds(
	status *kargoapi.StageStatus,
	shouldRemove func(string, kargoapi.AutoPromotionHold) bool,
) bool {
	var changed bool
	for origin, hold := range status.AutoPromotionHolds {
		if shouldRemove(origin, hold) {
			changed = true
			delete(status.AutoPromotionHolds, origin)
			continue
		}
	}
	if len(status.AutoPromotionHolds) == 0 {
		status.AutoPromotionHolds = nil
	}
	return changed
}

func autoPromotionHoldActor(ctx context.Context) string {
	if u, ok := user.InfoFromContext(ctx); ok {
		return api.FormatEventUserActor(u)
	}
	return ""
}
