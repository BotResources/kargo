package api

import (
	"context"
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

// AutoPromotionHoldTimesEqual reports whether two optional Kubernetes timestamps
// refer to the same instant, treating two nil values as equal. It is the single
// canonical comparator shared by every auto-promotion hold identity check.
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
// present it returns an *AutoPromotionHoldExistsError without changing state.
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

	now := metav1.Now()
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
	var existing *kargoapi.AutoPromotionHold
	if err := patchStageAutoPromotionHolds(
		ctx,
		c,
		stageKey,
		func(stage *kargoapi.Stage) bool {
			existing = nil
			if h, ok := stage.Status.GetAutoPromotionHold(freight.Origin); ok {
				existing = &h
				return false
			}
			if stage.Status.AutoPromotionHolds == nil {
				stage.Status.AutoPromotionHolds = make(map[string]kargoapi.AutoPromotionHold, 1)
			}
			stage.Status.AutoPromotionHolds[freight.Origin.String()] = hold
			return true
		},
	); err != nil {
		return fmt.Errorf("create auto-promotion hold: %w", err)
	}
	if existing != nil {
		return &AutoPromotionHoldExistsError{Origin: freight.Origin, State: existing.State}
	}

	if promotion.Annotations == nil {
		promotion.Annotations = make(map[string]string, 1)
	}
	promotion.Annotations[kargoapi.AnnotationKeyRollback] = kargoapi.AnnotationValueTrue

	if err := createPromotion(ctx, promotion); err != nil {
		// Only roll the hold back when the Promotion definitely did not persist;
		// otherwise an orphaned hold would block auto-promotion indefinitely.
		if !promotionCreateMayHavePersisted(err) {
			if cleanupErr := patchStageAutoPromotionHolds(
				ctx,
				c,
				stageKey,
				func(stage *kargoapi.Stage) bool {
					return removeMatchingAutoPromotionHold(&stage.Status, freight.Origin, hold)
				},
			); cleanupErr != nil {
				return fmt.Errorf(
					"create rollback Promotion: %w; clean up pending auto-promotion hold: %w",
					err, cleanupErr,
				)
			}
		}
		return fmt.Errorf("create rollback Promotion: %w", err)
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
		return fmt.Errorf("client must not be nil")
	case promotion == nil:
		return fmt.Errorf("promotion must not be nil")
	case promotion.Name == "":
		return fmt.Errorf("promotion must be named; build it with kargo.NewPromotionBuilder")
	case stageKey.Namespace == "" || stageKey.Name == "":
		return fmt.Errorf("stage key must have a namespace and name")
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
		return fmt.Errorf("a held rollback Promotion must not have an auto source")
	}
	if _, err := kargoapi.ParseFreightOriginKey(freight.Origin.String()); err != nil {
		return fmt.Errorf("invalid Freight origin: %w", err)
	}
	return nil
}

// removeMatchingAutoPromotionHold deletes the origin's hold only when it still
// exactly matches the pending hold this request created.
func removeMatchingAutoPromotionHold(
	status *kargoapi.StageStatus,
	origin kargoapi.FreightOrigin,
	expected kargoapi.AutoPromotionHold,
) bool {
	key := origin.String()
	hold, ok := status.AutoPromotionHolds[key]
	if !ok || !autoPromotionHoldMatchesPendingCreate(hold, expected) {
		return false
	}
	delete(status.AutoPromotionHolds, key)
	if len(status.AutoPromotionHolds) == 0 {
		status.AutoPromotionHolds = nil
	}
	return true
}

// autoPromotionHoldMatchesPendingCreate reports whether hold is still exactly the
// pending hold identified by expected, comparing only fields that survive API
// serialization. The Promotion name is unique to a single create attempt.
func autoPromotionHoldMatchesPendingCreate(
	hold kargoapi.AutoPromotionHold,
	expected kargoapi.AutoPromotionHold,
) bool {
	return hold.State == kargoapi.AutoPromotionHoldStatePending &&
		hold.PromotionName == expected.PromotionName &&
		hold.FreightName == expected.FreightName &&
		hold.Origin.Equals(&expected.Origin)
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

// patchStageAutoPromotionHolds mutates a Stage's status under optimistic locking,
// retrying on conflict. mutate is re-run against each fresh snapshot and reports
// whether it changed anything; when it returns false, no patch is sent.
func patchStageAutoPromotionHolds(
	ctx context.Context,
	c client.Client,
	key client.ObjectKey,
	mutate func(*kargoapi.Stage) bool,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		stage := &kargoapi.Stage{}
		if err := c.Get(ctx, key, stage); err != nil {
			return err
		}
		original := stage.DeepCopy()
		if !mutate(stage) {
			return nil
		}
		return c.Status().Patch(
			ctx,
			stage,
			client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
		)
	})
}
