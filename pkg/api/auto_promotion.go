package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/pkg/pattern"
)

// AutoPromotionBlockedByHoldMessage is the terminal Promotion message used
// when an auto-promotion is aborted because a Stage hold superseded it.
const AutoPromotionBlockedByHoldMessage = "auto-promotion superseded by an auto-promotion hold"

// IsAutoPromotionEnabled returns whether the ProjectConfig enables
// auto-promotion for the supplied Stage metadata.
func IsAutoPromotionEnabled(
	ctx context.Context,
	c client.Client,
	stage metav1.ObjectMeta,
) (bool, error) {
	projectCfg := &kargoapi.ProjectConfig{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      stage.Namespace,
		Namespace: stage.Namespace,
	}, projectCfg); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("error getting ProjectConfig for Project %q: %w", stage.Namespace, err)
	}

	for _, policy := range projectCfg.Spec.PromotionPolicies {
		if policy.StageSelector == nil {
			// Maintain backward compatibility with older versions of the
			// PromotionPolicy where the selector was not available.
			policy.StageSelector = &kargoapi.PromotionPolicySelector{
				Name: policy.Stage, // nolint:staticcheck
			}
		}

		if nameSelector := policy.StageSelector.Name; nameSelector != "" {
			m, err := pattern.ParseNamePattern(nameSelector)
			if err != nil {
				return false, fmt.Errorf("error parsing PromotionPolicy name pattern %q: %w", nameSelector, err)
			}
			if !m.Matches(stage.Name) {
				continue
			}
		}

		if labelSelector := policy.StageSelector.LabelSelector; labelSelector != nil {
			s, err := metav1.LabelSelectorAsSelector(labelSelector)
			if err != nil {
				return false, fmt.Errorf("error parsing PromotionPolicy label selector %q: %w", labelSelector, err)
			}
			if !s.Matches(labels.Set(stage.Labels)) {
				continue
			}
		}

		return policy.AutoPromotionEnabled, nil
	}

	return false, nil
}

// SelectAutoPromotionCandidates returns the Freight selected by each requested
// origin's auto-promotion selection policy. This is the candidate selection
// decision only; callers still own write-side guards such as current-Freight,
// existing-Promotion, and hold checks.
func SelectAutoPromotionCandidates(
	stage *kargoapi.Stage,
	availableFreight []kargoapi.Freight,
) (map[string]kargoapi.Freight, error) {
	availableByOrigin := make(map[string][]kargoapi.Freight)
	for _, freight := range availableFreight {
		origin := freight.Origin.String()
		availableByOrigin[origin] = append(availableByOrigin[origin], freight)
	}

	candidates := make(map[string]kargoapi.Freight)
	for _, req := range stage.Spec.RequestedFreight {
		origin := req.Origin.String()
		freight := availableByOrigin[origin]
		if len(freight) == 0 {
			continue
		}
		if req.Sources.AutoPromotionOptions != nil &&
			req.Sources.AutoPromotionOptions.SelectionPolicy == kargoapi.AutoPromotionSelectionPolicyMatchUpstream &&
			len(freight) > 1 {
			return nil, fmt.Errorf(
				"unexpectedly found %d available Freight running immediately "+
					"upstream from Stage %q in namespace %q; this should not be possible",
				len(freight), stage.Name, stage.Namespace,
			)
		}

		slices.SortFunc(freight, func(lhs, rhs kargoapi.Freight) int {
			cmp := rhs.CreationTimestamp.Compare(lhs.CreationTimestamp.Time)
			if cmp != 0 {
				return cmp
			}
			return strings.Compare(rhs.Name, lhs.Name)
		})
		candidates[origin] = freight[0]
	}
	return candidates, nil
}

// AutoPromotionHoldIdentityMatches returns whether hold still identifies the
// same auto-promotion hold as expected.
func AutoPromotionHoldIdentityMatches(
	hold kargoapi.AutoPromotionHold,
	expected kargoapi.AutoPromotionHold,
) bool {
	return hold.FreightName == expected.FreightName &&
		hold.Origin.Equals(&expected.Origin) &&
		hold.PromotionName == expected.PromotionName &&
		hold.PromotionUID == expected.PromotionUID &&
		AutoPromotionHoldTimesEqual(hold.CreatedAt, expected.CreatedAt)
}

// AutoPromotionHoldsEqual reports whether two holds are identical in every
// field: the identity fields compared by AutoPromotionHoldIdentityMatches
// plus State, Actor, and Reason. It is full-struct equality.
func AutoPromotionHoldsEqual(a, b kargoapi.AutoPromotionHold) bool {
	return AutoPromotionHoldIdentityMatches(a, b) &&
		a.State == b.State &&
		a.Actor == b.Actor &&
		a.Reason == b.Reason
}

// AutoPromotionHoldTimesEqual reports whether two optional Kubernetes
// timestamps refer to the same instant, treating two nil values as equal.
func AutoPromotionHoldTimesEqual(lhs *metav1.Time, rhs *metav1.Time) bool {
	switch {
	case lhs == nil && rhs == nil:
		return true
	case lhs == nil || rhs == nil:
		return false
	default:
		return lhs.Time.Equal(rhs.Time)
	}
}

// SetClearAutoPromotionHoldAnnotation annotates promo with a JSON snapshot of
// hold's identity so that, when promo succeeds, the Stage controller clears
// exactly that hold and no other.
func SetClearAutoPromotionHoldAnnotation(
	promo *kargoapi.Promotion,
	hold kargoapi.AutoPromotionHold,
) {
	req := &kargoapi.ClearAutoPromotionHoldRequest{
		Origin:        hold.Origin,
		PromotionName: hold.PromotionName,
		PromotionUID:  hold.PromotionUID,
		CreatedAt:     hold.CreatedAt,
	}
	if promo.Annotations == nil {
		promo.Annotations = make(map[string]string, 1)
	}
	promo.Annotations[kargoapi.AnnotationKeyClearAutoPromotionHold] = req.String()
}

// ClearAutoPromotionHoldRequestFromPromotion returns the clear-hold request
// annotated on promo, or nil when promo carries no such annotation or its
// value cannot be parsed.
func ClearAutoPromotionHoldRequestFromPromotion(
	promo *kargoapi.Promotion,
) *kargoapi.ClearAutoPromotionHoldRequest {
	value := promo.Annotations[kargoapi.AnnotationKeyClearAutoPromotionHold]
	if value == "" {
		return nil
	}
	req := &kargoapi.ClearAutoPromotionHoldRequest{}
	if err := json.Unmarshal([]byte(value), req); err != nil {
		return nil
	}
	return req
}

// HoldMatchesClearAnnotation reports whether promo's clear-hold annotation
// identifies exactly hold, i.e. whether a successful non-auto Promotion is
// clearing the same Active hold that was observed when promo was created.
// This prevents older Promotions from clearing newer rollback intent.
func HoldMatchesClearAnnotation(
	hold kargoapi.AutoPromotionHold,
	promo *kargoapi.Promotion,
) bool {
	if hold.State != kargoapi.AutoPromotionHoldStateActive {
		return false
	}
	req := ClearAutoPromotionHoldRequestFromPromotion(promo)
	if req == nil {
		return false
	}
	if hold.PromotionName == "" || req.PromotionName == "" {
		return false
	}
	return hold.Origin.Equals(&req.Origin) &&
		hold.PromotionName == req.PromotionName &&
		hold.PromotionUID == req.PromotionUID &&
		AutoPromotionHoldTimesEqual(hold.CreatedAt, req.CreatedAt) &&
		(hold.CreatedAt == nil || !promo.CreationTimestamp.Time.Before(hold.CreatedAt.Time))
}

// AutoPromotionHoldOptions configures a hold created by
// CreatePendingAutoPromotionHold.
type AutoPromotionHoldOptions struct {
	// Actor identifies who or what caused the hold. Optional.
	Actor string
	// Reason is a human-readable explanation recorded on the hold. Optional.
	Reason string
	// CreatePromotion creates the linked rollback Promotion. When nil, c.Create
	// is used. The API server overrides this so the Promotion is created with the
	// requesting user's authorizing client while Stage status is still written
	// with its internal client.
	CreatePromotion func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
}

// AutoPromotionHoldExistsError reports that an origin already has an
// auto-promotion hold, so a new rollback must not overwrite it. The API server
// maps this to a 409; a controller may treat it as "another rollback is already
// in flight, skip".
type AutoPromotionHoldExistsError struct {
	Origin kargoapi.FreightOrigin
	State  kargoapi.AutoPromotionHoldState
}

func (e *AutoPromotionHoldExistsError) Error() string {
	return fmt.Sprintf(
		"auto-promotion hold already exists for origin %q in state %q",
		e.Origin.String(),
		e.State,
	)
}

// CreatePendingAutoPromotionHold records a Pending AutoPromotionHold pinning
// freight.Origin to freight on the Stage identified by stageKey, then creates
// the linked, rollback-annotated Promotion.
//
// The hold is always written before the Promotion is created, so the Promotion
// controller's enforcement gate sees it and cannot let an auto-promotion of
// newer Freight stomp the rollback in the window between the two writes. If
// creating the Promotion fails without persisting, the pending hold is rolled
// back; if it might have persisted the hold is kept and left for the Stage
// controller to reconcile.
//
// It never overwrites an existing hold for the origin: when one is already
// present it returns an error wrapping an *AutoPromotionHoldExistsError
// (discoverable with errors.As) without changing state.
//
// promotion must be a non-auto Promotion of freight on stageKey's Stage and must
// already be named (build it with kargo.NewPromotionBuilder); these invariants
// are validated up front to keep the hold and its Promotion coherent. Status
// writes use c, so callers pass whichever client is allowed to patch Stage
// status (a controller's own client, or the API server's internal client).
func CreatePendingAutoPromotionHold(
	ctx context.Context,
	c client.Client,
	stageKey client.ObjectKey,
	promotion *kargoapi.Promotion,
	freight kargoapi.Freight,
	opts AutoPromotionHoldOptions,
) error {
	if err := validateAutoPromotionHoldRequest(c, stageKey, promotion, freight); err != nil {
		return err
	}

	createPromotion := opts.CreatePromotion
	if createPromotion == nil {
		createPromotion = c.Create
	}

	// Second-truncate the timestamp so the in-memory hold is identical to what
	// every reader sees after the API server's RFC3339 serialization. This is
	// what lets the cleanup path below re-find the hold by exact identity.
	now := metav1.Now().Rfc3339Copy()
	hold := kargoapi.AutoPromotionHold{
		FreightName:   freight.Name,
		Origin:        freight.Origin,
		State:         kargoapi.AutoPromotionHoldStatePending,
		PromotionName: promotion.Name,
		Actor:         opts.Actor,
		Reason:        opts.Reason,
		CreatedAt:     &now,
	}

	// Hold-first. The only precondition checked against live state is that no
	// hold already exists for this origin; an in-flight rollback owns it.
	if _, _, err := PatchStageAutoPromotionHolds(
		ctx,
		c,
		c,
		stageKey,
		func(status *kargoapi.StageStatus) (bool, error) {
			if existing, ok := status.GetAutoPromotionHold(freight.Origin); ok {
				return false, &AutoPromotionHoldExistsError{
					Origin: freight.Origin,
					State:  existing.State,
				}
			}
			if status.AutoPromotionHolds == nil {
				status.AutoPromotionHolds = make(map[string]kargoapi.AutoPromotionHold, 1)
			}
			status.AutoPromotionHolds[freight.Origin.String()] = hold
			return true, nil
		},
	); err != nil {
		return fmt.Errorf("error creating auto-promotion hold: %w", err)
	}

	if promotion.Annotations == nil {
		promotion.Annotations = make(map[string]string, 1)
	}
	promotion.Annotations[kargoapi.AnnotationKeyRollback] = kargoapi.AnnotationValueTrue

	if err := createPromotion(ctx, promotion); err != nil {
		// Only roll the hold back when the Promotion definitely did not persist;
		// otherwise an orphaned hold would block auto-promotion indefinitely.
		if !promotionCreateMayHavePersisted(err) {
			if _, _, cleanupErr := PatchStageAutoPromotionHolds(
				ctx,
				c,
				c,
				stageKey,
				func(status *kargoapi.StageStatus) (bool, error) {
					return removeMatchingAutoPromotionHold(status, freight.Origin, hold), nil
				},
			); cleanupErr != nil {
				return fmt.Errorf(
					"error creating rollback Promotion: %w; error cleaning up pending auto-promotion hold: %w",
					err, cleanupErr,
				)
			}
		}
		return fmt.Errorf("error creating rollback Promotion: %w", err)
	}
	return nil
}

// validateAutoPromotionHoldRequest rejects inputs that would produce an
// incoherent hold or a Promotion the enforcement gate would abort, turning easy
// misuse into a clear error before any state is written.
func validateAutoPromotionHoldRequest(
	c client.Client,
	stageKey client.ObjectKey,
	promotion *kargoapi.Promotion,
	freight kargoapi.Freight,
) error {
	switch {
	case c == nil:
		return errors.New("client must not be nil")
	case promotion == nil:
		return errors.New("promotion must not be nil")
	case promotion.Name == "":
		return errors.New("promotion must be named; build it with kargo.NewPromotionBuilder")
	case stageKey.Namespace == "" || stageKey.Name == "":
		return errors.New("stage key must have a namespace and name")
	case promotion.Namespace != stageKey.Namespace || promotion.Spec.Stage != stageKey.Name:
		return fmt.Errorf(
			"promotion %q targets Stage %q/%q but the hold is for %q/%q",
			promotion.Name,
			promotion.Namespace, promotion.Spec.Stage,
			stageKey.Namespace, stageKey.Name,
		)
	case promotion.Spec.Freight != freight.Name:
		return fmt.Errorf(
			"promotion promotes Freight %q but the hold pins Freight %q",
			promotion.Spec.Freight, freight.Name,
		)
	case promotion.Spec.Source == kargoapi.PromotionSourceAuto:
		return errors.New("a held rollback Promotion must not have an auto source")
	}
	if err := freight.Origin.Validate(); err != nil {
		return fmt.Errorf("invalid Freight origin: %w", err)
	}
	return nil
}

// removeMatchingAutoPromotionHold deletes the origin's hold only when it is
// still Pending and its identity exactly matches the pending hold this
// request created. CreatedAt round-trips intact because it is written
// second-truncated, and PromotionUID is empty on both sides because cleanup
// only runs when the Promotion definitely did not persist.
func removeMatchingAutoPromotionHold(
	status *kargoapi.StageStatus,
	origin kargoapi.FreightOrigin,
	expected kargoapi.AutoPromotionHold,
) bool {
	hold, ok := status.GetAutoPromotionHold(origin)
	if !ok ||
		hold.State != kargoapi.AutoPromotionHoldStatePending ||
		!AutoPromotionHoldIdentityMatches(hold, expected) {
		return false
	}
	status.DeleteAutoPromotionHold(origin.String())
	return true
}

// promotionCreateMayHavePersisted reports whether a create error may have been
// returned after the API server persisted the Promotion. This guards a real
// correctness boundary: if the Promotion actually persisted but we deleted the
// pending hold, nothing would block auto-promotion from stomping the rollback.
// So when in doubt we keep the hold and let the Stage controller reconcile the
// partial state (it abandons a pending hold whose Promotion never appears after
// a grace period).
func promotionCreateMayHavePersisted(err error) bool {
	if apierrors.IsAlreadyExists(err) {
		return true
	}
	if apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsUnexpectedServerError(err) {
		return true
	}
	// Anything that is not a structured Kubernetes API error (e.g. a transport
	// failure) is ambiguous about whether the write landed; bias toward keeping
	// the hold.
	var statusErr *apierrors.StatusError
	return !errors.As(err, &statusErr)
}

// PatchStageAutoPromotionHolds mutates the auto-promotion holds in the status
// of the Stage identified by key, patching under optimistic locking and
// retrying on conflict.
//
// reader fetches each fresh Stage snapshot while writer sends the status
// patch; they are distinct parameters so a controller can read through a
// cache-bypassing API reader while writing with its regular client. Callers
// with a single suitable client pass it as both. A NotFound error from reader
// propagates so each caller can decide whether it is tolerable.
//
// mutate is re-run against each fresh snapshot and reports whether it changed
// anything; when it reports false, no patch is sent. An error returned by
// mutate aborts the retries and is returned, so closures can surface typed
// errors to callers. An empty AutoPromotionHolds map is normalized to nil
// before patching, relieving mutate of that chore.
//
// The returned map contains the Stage's holds as last observed (post-patch
// when one was sent) and the returned bool reports whether a patch was sent.
func PatchStageAutoPromotionHolds(
	ctx context.Context,
	reader client.Reader,
	writer client.Client,
	key client.ObjectKey,
	mutate func(*kargoapi.StageStatus) (bool, error),
) (map[string]kargoapi.AutoPromotionHold, bool, error) {
	var holds map[string]kargoapi.AutoPromotionHold
	var patched bool
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		holds = nil
		patched = false
		stage := &kargoapi.Stage{}
		if err := reader.Get(ctx, key, stage); err != nil {
			return err
		}
		original := stage.DeepCopy()
		changed, err := mutate(&stage.Status)
		if err != nil {
			return err
		}
		if !changed {
			holds = stage.Status.AutoPromotionHolds
			return nil
		}
		if len(stage.Status.AutoPromotionHolds) == 0 {
			stage.Status.AutoPromotionHolds = nil
		}
		if err = writer.Status().Patch(
			ctx,
			stage,
			client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
		); err != nil {
			return err
		}
		holds = stage.Status.AutoPromotionHolds
		patched = true
		return nil
	}); err != nil {
		return nil, false, err
	}
	return holds, patched, nil
}
