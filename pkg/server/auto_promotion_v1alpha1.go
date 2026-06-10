package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	key := client.ObjectKey{
		Namespace: c.Param("project"),
		Name:      c.Param("stage"),
	}

	var req resumeStageAutoPromotionRequest
	if !bindJSONOrError(c, &req) {
		return
	}
	if req.Origin == nil || req.Origin.Kind == "" || req.Origin.Name == "" {
		_ = c.Error(libhttp.ErrorStr(
			"origin kind and name are required",
			http.StatusBadRequest,
		))
		return
	}
	if err := req.Origin.Validate(); err != nil {
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

	if err := s.resumeAutoPromotionForOrigin(ctx, key, *req.Origin); err != nil {
		_ = c.Error(err)
		return
	}
	c.Status(http.StatusNoContent)
}

// resumeAutoPromotionForOrigin clears the Stage's active auto-promotion hold
// for origin. The caller must already have authorized the custom "promote"
// verb; reads and writes here use the internal client so users who can
// promote, but cannot read or patch Stages directly, can still resume
// automation. Errors that should not surface as a 500 carry their HTTP code
// via libhttp for the router's error middleware.
func (s *server) resumeAutoPromotionForOrigin(
	ctx context.Context,
	key client.ObjectKey,
	origin kargoapi.FreightOrigin,
) error {
	stage := &kargoapi.Stage{}
	if err := s.client.InternalClient().Get(ctx, key, stage); err != nil {
		if apierrors.IsNotFound(err) {
			return libhttp.ErrorStr(
				fmt.Sprintf("Stage %q not found in project %q", key.Name, key.Namespace),
				http.StatusNotFound,
			)
		}
		return err
	}

	hold, ok := stage.Status.GetAutoPromotionHold(origin)
	if !ok {
		return libhttp.ErrorStr(
			"Stage has no active auto-promotion hold for the requested origin",
			http.StatusNotFound,
		)
	}
	if hold.State == kargoapi.AutoPromotionHoldStatePending {
		return libhttp.ErrorStr(
			fmt.Sprintf(
				"auto-promotion cannot be resumed while pending hold Promotion %q is still settling",
				hold.PromotionName,
			),
			http.StatusConflict,
		)
	}

	// The patch below re-verifies the hold against live state, turning any
	// concurrent change into a 409. Stage status is controller/API-owned, not
	// directly user-writable.
	originKey := origin.String()
	changed, err := api.PatchStageAutoPromotionHolds(
		ctx,
		s.client.InternalClient(),
		s.client.InternalClient(),
		key,
		func(status *kargoapi.StageStatus) (bool, error) {
			live, ok := status.AutoPromotionHolds[originKey]
			if !ok ||
				live.State != kargoapi.AutoPromotionHoldStateActive ||
				!api.AutoPromotionHoldIdentityMatches(live, hold) {
				return false, nil
			}
			status.DeleteAutoPromotionHold(originKey)
			return true, nil
		},
	)
	if err != nil {
		return fmt.Errorf("clear auto-promotion holds: %w", err)
	}
	if !changed {
		return libhttp.ErrorStr(
			"auto-promotion hold changed; reload and try again",
			http.StatusConflict,
		)
	}

	// A refresh failure never fails the request; the hold is already cleared
	// and the refresh only nudges the Stage controller to act sooner.
	if _, err = api.RefreshStage(ctx, s.client.InternalClient(), key); err != nil {
		logging.LoggerFromContext(ctx).Error(
			err,
			"error refreshing Stage after resuming auto-promotion",
			"stage", key.Name,
		)
	}
	return nil
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
	if candidate, ok := candidates[origin.String()]; ok {
		return &candidate, nil
	}
	return nil, nil
}

func (s *server) getAutoPromotionCandidates(
	ctx context.Context,
	stage *kargoapi.Stage,
) (map[string]kargoapi.Freight, error) {
	// Candidate calculation is a mechanical Stage-level decision. Endpoint
	// handlers authorize the user before reaching this path, then use the
	// internal client so promote permissions do not also require unrelated
	// ProjectConfig, Warehouse, and Freight read permissions.
	enabled, err := s.isAutoPromotionEnabledFn(ctx, s.client.InternalClient(), stage.ObjectMeta)
	if err != nil {
		return nil, fmt.Errorf("check auto-promotion enablement: %w", err)
	}
	if !enabled {
		return nil, nil
	}

	availableFreight, err := s.getAutoPromotionAvailableFreightForStageFn(ctx, stage)
	if err != nil {
		return nil, fmt.Errorf("get available Freight for Stage: %w", err)
	}

	candidates, err := api.SelectAutoPromotionCandidates(stage, availableFreight)
	if err != nil {
		return nil, fmt.Errorf("select auto-promotion candidates: %w", err)
	}
	return candidates, nil
}

func (s *server) getAutoPromotionAvailableFreightForStage(
	ctx context.Context,
	stage *kargoapi.Stage,
) ([]kargoapi.Freight, error) {
	return api.ListFreightAvailableToStage(ctx, s.client.InternalClient(), stage)
}

func autoPromotionHoldActor(ctx context.Context) string {
	if u, ok := user.InfoFromContext(ctx); ok {
		return api.FormatEventUserActor(u)
	}
	return ""
}
