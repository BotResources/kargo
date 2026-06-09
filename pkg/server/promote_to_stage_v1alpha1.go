package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcv1alpha1 "github.com/akuity/kargo/api/service/v1alpha1"
	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/pkg/api"
	"github.com/akuity/kargo/pkg/event"
	libhttp "github.com/akuity/kargo/pkg/http"
	"github.com/akuity/kargo/pkg/kargo"
	"github.com/akuity/kargo/pkg/logging"
	"github.com/akuity/kargo/pkg/server/user"
)

// PromoteToStage creates a Promotion resource to transition a specified Stage
// into the state represented by the specified Freight.
func (s *server) PromoteToStage(
	ctx context.Context,
	req *connect.Request[svcv1alpha1.PromoteToStageRequest],
) (*connect.Response[svcv1alpha1.PromoteToStageResponse], error) {
	project := req.Msg.GetProject()
	if err := validateFieldNotEmpty("project", project); err != nil {
		return nil, err
	}

	stageName := req.Msg.GetStage()
	if err := validateFieldNotEmpty("stage", stageName); err != nil {
		return nil, err
	}

	freightName := req.Msg.GetFreight()
	freightAlias := req.Msg.GetFreightAlias()
	if (freightName == "" && freightAlias == "") || (freightName != "" && freightAlias != "") {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("exactly one of freight or freightAlias should not be empty"),
		)
	}

	if err := s.validateProjectExistsFn(ctx, project); err != nil {
		return nil, err
	}

	if err := s.authorizeFn(
		ctx,
		"promote",
		kargoapi.GroupVersion.WithResource("stages"),
		"",
		types.NamespacedName{
			Namespace: project,
			Name:      stageName,
		},
	); err != nil {
		return nil, err
	}

	stage, err := s.getStageFn(
		ctx,
		s.client,
		types.NamespacedName{
			Namespace: project,
			Name:      stageName,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("get stage: %w", err)
	}
	if stage == nil {
		// nolint:staticcheck
		return nil, connect.NewError(
			connect.CodeNotFound,
			fmt.Errorf(
				"Stage %q not found in namespace %q",
				stageName,
				project,
			),
		)
	}

	freight, err := s.getFreightByNameOrAliasFn(
		ctx,
		s.client,
		project,
		freightName,
		freightAlias,
	)
	if err != nil {
		return nil, fmt.Errorf("get freight: %w", err)
	}
	if freight == nil {
		if freightName != "" {
			err = fmt.Errorf("freight %q not found in namespace %q", freightName, project)
		} else {
			err = fmt.Errorf("freight with alias %q not found in namespace %q", freightAlias, project)
		}
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	if !s.isFreightAvailableFn(stage, freight) {
		// nolint:staticcheck
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf(
				"Freight %q is not available to Stage %q",
				freightName,
				stageName,
			),
		)
	}

	promotion, err := s.createStagePromotion(
		ctx,
		stage,
		freight,
		stagePromotionOptions{},
	)
	if err != nil {
		return nil, stagePromotionConnectError(err)
	}
	s.recordPromotionCreatedEvent(ctx, promotion, freight)
	return connect.NewResponse(&svcv1alpha1.PromoteToStageResponse{
		Promotion: promotion,
	}), nil
}

func (s *server) isFreightAvailable(
	stage *kargoapi.Stage,
	freight *kargoapi.Freight,
) bool {
	return stage.IsFreightAvailable(freight)
}

func (s *server) recordPromotionCreatedEvent(
	ctx context.Context,
	p *kargoapi.Promotion,
	f *kargoapi.Freight,
) {
	if s.sender == nil {
		return
	}

	var actor string
	msg := fmt.Sprintf("Promotion created for Stage %q", p.Spec.Stage)
	if u, ok := user.InfoFromContext(ctx); ok {
		actor = api.FormatEventUserActor(u)
		msg += fmt.Sprintf(" by %q", actor)
	}

	evt := event.NewPromotionCreated(msg, actor, p, f)
	if err := s.sender.Send(ctx, evt); err != nil {
		logging.LoggerFromContext(ctx).Error(err, "Error when publishing new promotion event")
	}
}

// promoteToStageRequest represents the request body for the PromoteToStage REST endpoint.
type promoteToStageRequest struct {
	Freight               string `json:"freight,omitempty"`
	FreightAlias          string `json:"freightAlias,omitempty"`
	ExpectedAutoCandidate string `json:"expectedAutoCandidate,omitempty"`
	Reason                string `json:"reason,omitempty"`
} // @name PromoteToStageRequest

// @id PromoteToStage
// @Summary Promote to Stage
// @Description Create a Promotion resource to transition a specified Stage into
// @Description the state represented by the specified Freight.
// @Tags Core, Project-Level
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param project path string true "Project name"
// @Param stage path string true "Stage name"
// @Param body body promoteToStageRequest true "Promote request"
// @Success 201 {object} kargoapi.Promotion "Promotion resource (github.com/akuity/kargo/api/v1alpha1.Promotion)"
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 504 {object} ErrorResponse
// @Router /v1beta1/projects/{project}/stages/{stage}/promotions [post]
func (s *server) promoteToStage(c *gin.Context) {
	ctx := c.Request.Context()
	project := c.Param("project")
	stageName := c.Param("stage")

	var req promoteToStageRequest
	if !bindJSONOrError(c, &req) {
		return
	}

	// Validate that exactly one of freight or freightAlias is provided
	if (req.Freight == "" && req.FreightAlias == "") || (req.Freight != "" && req.FreightAlias != "") {
		_ = c.Error(libhttp.ErrorStr(
			"exactly one of freight or freightAlias must be provided",
			http.StatusBadRequest,
		))
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if len(req.Reason) > kargoapi.AutoPromotionHoldReasonMaxLength {
		_ = c.Error(libhttp.ErrorStr(
			fmt.Sprintf(
				"reason cannot be longer than %d characters",
				kargoapi.AutoPromotionHoldReasonMaxLength,
			),
			http.StatusBadRequest,
		))
		return
	}

	if err := s.authorizeFn(
		ctx,
		"promote",
		kargoapi.GroupVersion.WithResource("stages"),
		"",
		types.NamespacedName{
			Namespace: project,
			Name:      stageName,
		},
	); err != nil {
		_ = c.Error(err)
		return
	}

	// Get the Stage
	stage, ok := s.getRESTStage(ctx, project, stageName, c)
	if !ok {
		return
	}

	// Get the Freight by name or alias
	var freight *kargoapi.Freight
	if req.Freight != "" {
		freight = &kargoapi.Freight{}
		if err := s.client.Get(ctx, client.ObjectKey{Namespace: project, Name: req.Freight}, freight); err != nil {
			if apierrors.IsNotFound(err) {
				_ = c.Error(libhttp.ErrorStr(
					fmt.Sprintf("Freight %q not found in project %q", req.Freight, project),
					http.StatusNotFound,
				))
				return
			}
			_ = c.Error(err)
			return
		}
	} else {
		// Search by alias
		list := &kargoapi.FreightList{}
		if err := s.client.List(
			ctx,
			list,
			client.InNamespace(project),
			client.MatchingLabels{kargoapi.LabelKeyAlias: req.FreightAlias},
		); err != nil {
			_ = c.Error(err)
			return
		}
		if len(list.Items) == 0 {
			_ = c.Error(libhttp.ErrorStr(
				fmt.Sprintf("Freight with alias %q not found in project %q", req.FreightAlias, project),
				http.StatusNotFound,
			))
			return
		}
		freight = &list.Items[0]
	}

	// Validate that the Freight is available to the Stage
	if !stage.IsFreightAvailable(freight) {
		_ = c.Error(libhttp.ErrorStr(
			fmt.Sprintf("Freight %q is not available to Stage %q", freight.Name, stageName),
			http.StatusBadRequest,
		))
		return
	}

	promotion, err := s.createStagePromotion(
		ctx,
		stage,
		freight,
		stagePromotionOptions{
			ExpectedAutoCandidate: req.ExpectedAutoCandidate,
			Reason:                req.Reason,
		},
	)
	if err != nil {
		_ = c.Error(stagePromotionRESTError(err))
		return
	}
	s.recordPromotionCreatedEvent(ctx, promotion, freight)

	c.JSON(http.StatusCreated, promotion)
}

// stagePromotionOptions carries optional REST-only safeguards for creating a
// Promotion directly to a Stage.
type stagePromotionOptions struct {
	ExpectedAutoCandidate string
	Reason                string
}

// stagePromotionConflictError reports user-retryable conflicts detected before
// creating a Promotion.
type stagePromotionConflictError struct {
	message string
}

func (s *stagePromotionConflictError) Error() string {
	return s.message
}

func newStagePromotionConflictError(format string, args ...any) error {
	return &stagePromotionConflictError{message: fmt.Sprintf(format, args...)}
}

// createStagePromotion creates a non-auto Promotion and handles the
// auto-promotion hold side effects implied by the selected Freight. Selecting
// Freight other than the current auto-promotion candidate records a pending
// hold before the Promotion is created. Selecting the current candidate records
// an annotation that lets the Stage controller clear an existing active hold
// after the Promotion succeeds.
func (s *server) createStagePromotion(
	ctx context.Context,
	stage *kargoapi.Stage,
	freight *kargoapi.Freight,
	opts stagePromotionOptions,
) (*kargoapi.Promotion, error) {
	promotion, err := kargo.NewPromotionBuilder(s.client).Build(ctx, *stage, freight.Name)
	if err != nil {
		return nil, fmt.Errorf("build promotion: %w", err)
	}

	key := client.ObjectKey{Namespace: stage.Namespace, Name: stage.Name}
	if err = s.authorizeFn(
		ctx,
		"create",
		kargoapi.GroupVersion.WithResource("promotions"),
		"",
		client.ObjectKeyFromObject(promotion),
	); err != nil {
		return nil, err
	}

	candidate, err := s.getAutoPromotionCandidate(ctx, stage, freight.Origin)
	if err != nil {
		return nil, fmt.Errorf("get auto-promotion candidate: %w", err)
	}
	if err = checkExpectedAutoCandidate(opts.ExpectedAutoCandidate, candidate); err != nil {
		return nil, err
	}

	// Selecting Freight other than the current auto-promotion candidate is a
	// rollback: record a pending hold and create the rollback Promotion together.
	// Selecting the candidate instead lets the Stage controller clear any
	// existing active hold once the Promotion succeeds. Stage status is written
	// with the internal client because users cannot patch it directly, while the
	// Promotion is created with the user's authorizing client.
	switch {
	case candidate != nil && candidate.Name != freight.Name:
		if err = api.CreatePendingAutoPromotionHold(
			ctx,
			s.client.InternalClient(),
			key,
			promotion,
			*freight,
			api.AutoPromotionHoldOptions{
				Actor:           autoPromotionHoldActor(ctx),
				Reason:          opts.Reason,
				CreatePromotion: s.createPromotionFn,
			},
		); err != nil {
			var exists *api.AutoPromotionHoldExistsError
			if errors.As(err, &exists) {
				return nil, newStagePromotionConflictError(
					"auto-promotion is already %s for origin %q; wait for the "+
						"current rollback to settle or resume auto-promotion before "+
						"creating another rollback",
					strings.ToLower(string(exists.State)),
					exists.Origin.String(),
				)
			}
			return nil, statusOrInternalError(err)
		}
		// The caller was authorized for the custom "promote" verb by the
		// endpoint handler. The internal client performs the mechanical refresh
		// annotation write because users are not generally allowed to patch
		// Stages. A refresh failure never fails the request; the rollback
		// Promotion and its hold are already in place.
		if _, err = api.RefreshStage(ctx, s.client.InternalClient(), key); err != nil {
			logging.LoggerFromContext(ctx).Error(
				err,
				"error refreshing Stage after creating rollback Promotion",
				"stage", stage.Name,
				"promotion", promotion.Name,
			)
		}
		return promotion, nil
	case candidate != nil:
		if err = s.annotateAutoPromotionHoldClearIfCurrent(
			ctx,
			key,
			freight,
			promotion,
			opts,
		); err != nil {
			return nil, err
		}
	}

	if err = s.createPromotionFn(ctx, promotion); err != nil {
		return nil, createPromotionError(err)
	}
	return promotion, nil
}

// annotateAutoPromotionHoldClearIfCurrent re-reads the Stage through the
// internal client -- the cached Stage that nominated the selected Freight as
// the current candidate may be stale -- and re-checks the caller's candidate
// preconditions against that live snapshot. When the live snapshot carries an
// active hold for the Freight's origin, promotion snapshots its exact identity
// so the Stage controller clears that hold -- and no other -- if the Promotion
// succeeds. A pending live hold is a conflict; no live hold means there is
// nothing to clear.
func (s *server) annotateAutoPromotionHoldClearIfCurrent(
	ctx context.Context,
	key client.ObjectKey,
	freight *kargoapi.Freight,
	promotion *kargoapi.Promotion,
	opts stagePromotionOptions,
) error {
	liveStage := &kargoapi.Stage{}
	if err := s.client.InternalClient().Get(ctx, key, liveStage); err != nil {
		return fmt.Errorf("get live Stage before clearing auto-promotion hold: %w", err)
	}
	liveCandidate, err := s.getAutoPromotionCandidate(ctx, liveStage, freight.Origin)
	if err != nil {
		return fmt.Errorf("get live auto-promotion candidate: %w", err)
	}
	if err = checkExpectedAutoCandidate(opts.ExpectedAutoCandidate, liveCandidate); err != nil {
		return err
	}
	if liveCandidate != nil && liveCandidate.Name != freight.Name {
		return newStagePromotionConflictError(
			"auto-promotion candidate changed to %q; reload and try again",
			liveCandidate.Name,
		)
	}

	liveHold, liveHeld := liveStage.Status.GetAutoPromotionHold(freight.Origin)
	if !liveHeld {
		return nil
	}
	if liveHold.State == kargoapi.AutoPromotionHoldStatePending {
		return newStagePromotionConflictError(
			"auto-promotion is pending for origin %q; wait for the "+
				"current rollback to settle before promoting the current candidate",
			freight.Origin.String(),
		)
	}
	api.SetClearAutoPromotionHoldAnnotation(promotion, liveHold)
	return nil
}

// checkExpectedAutoCandidate returns a conflict when a request's
// stale-candidate precondition no longer matches the current candidate.
func checkExpectedAutoCandidate(
	expected string,
	candidate *kargoapi.Freight,
) error {
	if expected == "" {
		return nil
	}
	currentCandidate := ""
	if candidate != nil {
		currentCandidate = candidate.Name
	}
	if currentCandidate == expected {
		return nil
	}
	return newStagePromotionConflictError(
		"auto-promotion candidate changed from %q to %q; reload and try again",
		expected,
		currentCandidate,
	)
}

func stagePromotionRESTError(err error) error {
	var conflictErr *stagePromotionConflictError
	if errors.As(err, &conflictErr) {
		return libhttp.ErrorStr(conflictErr.Error(), http.StatusConflict)
	}
	return err
}

func stagePromotionConnectError(err error) error {
	var conflictErr *stagePromotionConflictError
	if errors.As(err, &conflictErr) {
		return connect.NewError(connect.CodeFailedPrecondition, conflictErr)
	}
	return err
}

func createPromotionError(err error) error {
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		status := statusErr.ErrStatus
		status.Message = fmt.Sprintf("create promotion: %s", status.Message)
		return &apierrors.StatusError{ErrStatus: status}
	}
	return apierrors.NewInternalError(fmt.Errorf("create promotion: %w", err))
}

// statusOrInternalError returns err unchanged when it already carries a
// Kubernetes status (and thus an HTTP code), and wraps anything else as an
// internal error. Unlike createPromotionError it adds no prefix; it is for
// errors whose messages are already self-describing.
func statusOrInternalError(err error) error {
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		return err
	}
	return apierrors.NewInternalError(err)
}
