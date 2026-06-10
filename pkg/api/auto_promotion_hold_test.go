package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
)

func TestCreatePendingAutoPromotionHold(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kargoapi.AddToScheme(scheme))

	const (
		project = "fake-project"
		stage   = "fake-stage"
	)
	origin := kargoapi.FreightOrigin{
		Kind: kargoapi.FreightOriginKindWarehouse,
		Name: "fake-warehouse",
	}
	stageKey := client.ObjectKey{Namespace: project, Name: stage}
	freight := kargoapi.Freight{
		ObjectMeta: metav1.ObjectMeta{Name: "good-freight", Namespace: project},
		Origin:     origin,
	}

	// newPromotion returns a valid, named non-auto Promotion of freight on the
	// Stage, mirroring what kargo.NewPromotionBuilder produces.
	newPromotion := func() *kargoapi.Promotion {
		return &kargoapi.Promotion{
			ObjectMeta: metav1.ObjectMeta{Name: "fake-rollback", Namespace: project},
			Spec: kargoapi.PromotionSpec{
				Stage:   stage,
				Freight: freight.Name,
				Source:  kargoapi.PromotionSourceNonAuto,
			},
		}
	}
	newStage := func() *kargoapi.Stage {
		return &kargoapi.Stage{
			ObjectMeta: metav1.ObjectMeta{Name: stage, Namespace: project},
		}
	}

	testCases := []struct {
		name      string
		stage     *kargoapi.Stage
		promotion *kargoapi.Promotion
		opts      AutoPromotionHoldOptions
		assert    func(*testing.T, client.Client, *kargoapi.Promotion, error)
	}{
		{
			name:      "writes pending hold and creates rollback Promotion",
			stage:     newStage(),
			promotion: newPromotion(),
			opts:      AutoPromotionHoldOptions{Actor: "tester", Reason: "rolling back"},
			assert: func(t *testing.T, c client.Client, promo *kargoapi.Promotion, err error) {
				require.NoError(t, err)

				liveStage := &kargoapi.Stage{}
				require.NoError(t, c.Get(context.Background(), stageKey, liveStage))
				hold, ok := liveStage.Status.GetAutoPromotionHold(origin)
				require.True(t, ok)
				require.Equal(t, kargoapi.AutoPromotionHoldStatePending, hold.State)
				require.Equal(t, freight.Name, hold.FreightName)
				require.True(t, hold.Origin.Equals(&origin))
				require.Equal(t, promo.Name, hold.PromotionName)
				require.Equal(t, "tester", hold.Actor)
				require.Equal(t, "rolling back", hold.Reason)
				require.NotNil(t, hold.CreatedAt)
				// The timestamp must be second-truncated so the in-memory hold
				// is identical to its API-server-serialized form.
				require.True(t, hold.CreatedAt.Time.Equal(hold.CreatedAt.Rfc3339Copy().Time))

				livePromo := &kargoapi.Promotion{}
				require.NoError(t, c.Get(
					context.Background(),
					client.ObjectKeyFromObject(promo),
					livePromo,
				))
				require.Equal(
					t,
					kargoapi.AnnotationValueTrue,
					livePromo.Annotations[kargoapi.AnnotationKeyRollback],
				)
			},
		},
		{
			name: "refuses to overwrite an existing hold for the origin",
			stage: func() *kargoapi.Stage {
				s := newStage()
				s.Status.AutoPromotionHolds = map[string]kargoapi.AutoPromotionHold{
					origin.String(): {
						FreightName: "previous-freight",
						Origin:      origin,
						State:       kargoapi.AutoPromotionHoldStateActive,
					},
				}
				return s
			}(),
			promotion: newPromotion(),
			assert: func(t *testing.T, c client.Client, promo *kargoapi.Promotion, err error) {
				// The typed error must remain discoverable through the %w wrap.
				var exists *AutoPromotionHoldExistsError
				require.ErrorAs(t, err, &exists)
				require.Equal(t, kargoapi.AutoPromotionHoldStateActive, exists.State)
				require.ErrorContains(t, err, "error creating auto-promotion hold")

				// The existing hold is untouched and no Promotion was created.
				liveStage := &kargoapi.Stage{}
				require.NoError(t, c.Get(context.Background(), stageKey, liveStage))
				hold, _ := liveStage.Status.GetAutoPromotionHold(origin)
				require.Equal(t, "previous-freight", hold.FreightName)

				err = c.Get(context.Background(), client.ObjectKeyFromObject(promo), &kargoapi.Promotion{})
				require.True(t, apierrors.IsNotFound(err))
			},
		},
		{
			name:      "rolls back the pending hold when the create definitely fails",
			stage:     newStage(),
			promotion: newPromotion(),
			opts: AutoPromotionHoldOptions{
				CreatePromotion: func(context.Context, client.Object, ...client.CreateOption) error {
					return apierrors.NewBadRequest("nope")
				},
			},
			assert: func(t *testing.T, c client.Client, _ *kargoapi.Promotion, err error) {
				require.ErrorContains(t, err, "error creating rollback Promotion")

				liveStage := &kargoapi.Stage{}
				require.NoError(t, c.Get(context.Background(), stageKey, liveStage))
				_, ok := liveStage.Status.GetAutoPromotionHold(origin)
				require.False(t, ok)
			},
		},
		{
			name:      "keeps the pending hold when the create may have persisted",
			stage:     newStage(),
			promotion: newPromotion(),
			opts: AutoPromotionHoldOptions{
				CreatePromotion: func(context.Context, client.Object, ...client.CreateOption) error {
					return apierrors.NewServiceUnavailable("try again")
				},
			},
			assert: func(t *testing.T, c client.Client, _ *kargoapi.Promotion, err error) {
				require.ErrorContains(t, err, "error creating rollback Promotion")

				liveStage := &kargoapi.Stage{}
				require.NoError(t, c.Get(context.Background(), stageKey, liveStage))
				hold, ok := liveStage.Status.GetAutoPromotionHold(origin)
				require.True(t, ok)
				require.Equal(t, kargoapi.AutoPromotionHoldStatePending, hold.State)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(testCase.stage).
				WithStatusSubresource(&kargoapi.Stage{}).
				Build()
			err := CreatePendingAutoPromotionHold(
				t.Context(),
				c,
				stageKey,
				testCase.promotion,
				freight,
				testCase.opts,
			)
			testCase.assert(t, c, testCase.promotion, err)
		})
	}
}

func TestCreatePendingAutoPromotionHoldValidation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kargoapi.AddToScheme(scheme))

	const (
		project = "fake-project"
		stage   = "fake-stage"
	)
	origin := kargoapi.FreightOrigin{
		Kind: kargoapi.FreightOriginKindWarehouse,
		Name: "fake-warehouse",
	}
	stageKey := client.ObjectKey{Namespace: project, Name: stage}
	freight := kargoapi.Freight{
		ObjectMeta: metav1.ObjectMeta{Name: "good-freight", Namespace: project},
		Origin:     origin,
	}
	validPromotion := func() *kargoapi.Promotion {
		return &kargoapi.Promotion{
			ObjectMeta: metav1.ObjectMeta{Name: "fake-rollback", Namespace: project},
			Spec: kargoapi.PromotionSpec{
				Stage:   stage,
				Freight: freight.Name,
				Source:  kargoapi.PromotionSourceNonAuto,
			},
		}
	}

	testCases := []struct {
		name      string
		promotion *kargoapi.Promotion
		errString string
	}{
		{
			name:      "nil promotion",
			promotion: nil,
			errString: "promotion must not be nil",
		},
		{
			name: "unnamed promotion",
			promotion: func() *kargoapi.Promotion {
				p := validPromotion()
				p.Name = ""
				return p
			}(),
			errString: "promotion must be named",
		},
		{
			name: "promotion targets a different Stage",
			promotion: func() *kargoapi.Promotion {
				p := validPromotion()
				p.Spec.Stage = "other-stage"
				return p
			}(),
			errString: "but the hold is for",
		},
		{
			name: "promotion promotes different Freight",
			promotion: func() *kargoapi.Promotion {
				p := validPromotion()
				p.Spec.Freight = "other-freight"
				return p
			}(),
			errString: "but the hold pins Freight",
		},
		{
			name: "auto-sourced promotion",
			promotion: func() *kargoapi.Promotion {
				p := validPromotion()
				p.Spec.Source = kargoapi.PromotionSourceAuto
				return p
			}(),
			errString: "must not have an auto source",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(&kargoapi.Stage{
					ObjectMeta: metav1.ObjectMeta{Name: stage, Namespace: project},
				}).
				WithStatusSubresource(&kargoapi.Stage{}).
				Build()
			err := CreatePendingAutoPromotionHold(
				t.Context(),
				c,
				stageKey,
				testCase.promotion,
				freight,
				AutoPromotionHoldOptions{},
			)
			require.ErrorContains(t, err, testCase.errString)

			// Validation failures never write a hold.
			liveStage := &kargoapi.Stage{}
			require.NoError(t, c.Get(t.Context(), stageKey, liveStage))
			require.Empty(t, liveStage.Status.AutoPromotionHolds)
		})
	}

	t.Run("invalid Freight origin", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		err := CreatePendingAutoPromotionHold(
			t.Context(),
			c,
			stageKey,
			validPromotion(),
			kargoapi.Freight{ObjectMeta: metav1.ObjectMeta{Name: "good-freight"}},
			AutoPromotionHoldOptions{},
		)
		require.ErrorContains(t, err, "invalid Freight origin")
	})
}

func TestPromotionCreateMayHavePersisted(t *testing.T) {
	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "already exists",
			err: apierrors.NewAlreadyExists(
				kargoapi.GroupVersion.WithResource("promotions").GroupResource(),
				"fake-promotion",
			),
			want: true,
		},
		{
			name: "timeout",
			err:  apierrors.NewTimeoutError("timed out", 0),
			want: true,
		},
		{
			name: "server timeout",
			err: apierrors.NewServerTimeout(
				kargoapi.GroupVersion.WithResource("promotions").GroupResource(),
				"create",
				0,
			),
			want: true,
		},
		{
			name: "service unavailable",
			err:  apierrors.NewServiceUnavailable("temporarily unavailable"),
			want: true,
		},
		{
			name: "internal error",
			err:  apierrors.NewInternalError(errors.New("internal")),
			want: true,
		},
		{
			name: "unexpected server error",
			err: &apierrors.StatusError{ErrStatus: metav1.Status{
				Status: metav1.StatusFailure,
				Code:   http.StatusInternalServerError,
				Details: &metav1.StatusDetails{
					Causes: []metav1.StatusCause{{
						Type: metav1.CauseTypeUnexpectedServerResponse,
					}},
				},
			}},
			want: true,
		},
		{
			name: "bad request",
			err:  apierrors.NewBadRequest("invalid Promotion"),
		},
		{
			name: "forbidden",
			err: apierrors.NewForbidden(
				kargoapi.GroupVersion.WithResource("promotions").GroupResource(),
				"fake-promotion",
				errors.New("not allowed"),
			),
		},
		{
			name: "plain error",
			err:  errors.New("connection reset after write"),
			want: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, promotionCreateMayHavePersisted(testCase.err))
		})
	}
}

func TestAutoPromotionHoldsEqual(t *testing.T) {
	now := metav1.Now().Rfc3339Copy()
	newHold := func() kargoapi.AutoPromotionHold {
		return kargoapi.AutoPromotionHold{
			FreightName: "fake-freight",
			Origin: kargoapi.FreightOrigin{
				Kind: kargoapi.FreightOriginKindWarehouse,
				Name: "fake-warehouse",
			},
			State:         kargoapi.AutoPromotionHoldStateActive,
			PromotionName: "fake-rollback",
			PromotionUID:  "fake-uid",
			Actor:         "tester",
			Reason:        "rolling back",
			CreatedAt:     &now,
		}
	}

	testCases := []struct {
		name   string
		mutate func(*kargoapi.AutoPromotionHold)
		want   bool
	}{
		{
			name:   "identical holds",
			mutate: func(*kargoapi.AutoPromotionHold) {},
			want:   true,
		},
		{
			name: "equal times behind distinct pointers",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				createdAt := metav1.NewTime(now.Time)
				hold.CreatedAt = &createdAt
			},
			want: true,
		},
		{
			name: "different FreightName",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				hold.FreightName = "other-freight"
			},
		},
		{
			name: "different Origin",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				hold.Origin.Name = "other-warehouse"
			},
		},
		{
			name: "different State",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				hold.State = kargoapi.AutoPromotionHoldStatePending
			},
		},
		{
			name: "different PromotionName",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				hold.PromotionName = "other-rollback"
			},
		},
		{
			name: "different PromotionUID",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				hold.PromotionUID = "other-uid"
			},
		},
		{
			name: "different Actor",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				hold.Actor = "other-actor"
			},
		},
		{
			name: "different Reason",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				hold.Reason = "other-reason"
			},
		},
		{
			name: "different CreatedAt",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				createdAt := metav1.NewTime(now.Add(-time.Hour))
				hold.CreatedAt = &createdAt
			},
		},
		{
			name: "nil CreatedAt on one side",
			mutate: func(hold *kargoapi.AutoPromotionHold) {
				hold.CreatedAt = nil
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			other := newHold()
			testCase.mutate(&other)
			require.Equal(t, testCase.want, AutoPromotionHoldsEqual(newHold(), other))
		})
	}
}

func TestPatchStageAutoPromotionHolds(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kargoapi.AddToScheme(scheme))

	const (
		project = "fake-project"
		stage   = "fake-stage"
	)
	origin := kargoapi.FreightOrigin{
		Kind: kargoapi.FreightOriginKindWarehouse,
		Name: "fake-warehouse",
	}
	stageKey := client.ObjectKey{Namespace: project, Name: stage}
	hold := kargoapi.AutoPromotionHold{
		FreightName: "fake-freight",
		Origin:      origin,
		State:       kargoapi.AutoPromotionHoldStatePending,
	}
	newStage := func(holds map[string]kargoapi.AutoPromotionHold) *kargoapi.Stage {
		return &kargoapi.Stage{
			ObjectMeta: metav1.ObjectMeta{Name: stage, Namespace: project},
			Status:     kargoapi.StageStatus{AutoPromotionHolds: holds},
		}
	}

	testCases := []struct {
		name        string
		stage       *kargoapi.Stage
		interceptor interceptor.Funcs
		mutate      func(*kargoapi.StageStatus) (bool, error)
		assert      func(*testing.T, client.Client, bool, error)
	}{
		{
			name: "missing Stage propagates NotFound",
			mutate: func(*kargoapi.StageStatus) (bool, error) {
				return true, nil
			},
			assert: func(
				t *testing.T,
				_ client.Client,
				patched bool,
				err error,
			) {
				require.True(t, apierrors.IsNotFound(err))
				require.False(t, patched)
			},
		},
		{
			name:  "mutate error aborts and propagates",
			stage: newStage(map[string]kargoapi.AutoPromotionHold{origin.String(): hold}),
			mutate: func(status *kargoapi.StageStatus) (bool, error) {
				existing := status.AutoPromotionHolds[origin.String()]
				return false, &AutoPromotionHoldExistsError{
					Origin: existing.Origin,
					State:  existing.State,
				}
			},
			assert: func(
				t *testing.T,
				_ client.Client,
				patched bool,
				err error,
			) {
				var exists *AutoPromotionHoldExistsError
				require.ErrorAs(t, err, &exists)
				require.Equal(t, kargoapi.AutoPromotionHoldStatePending, exists.State)
				require.False(t, patched)
			},
		},
		{
			name:  "no change sends no patch",
			stage: newStage(map[string]kargoapi.AutoPromotionHold{origin.String(): hold}),
			mutate: func(*kargoapi.StageStatus) (bool, error) {
				return false, nil
			},
			assert: func(
				t *testing.T,
				c client.Client,
				patched bool,
				err error,
			) {
				require.NoError(t, err)
				require.False(t, patched)

				liveStage := &kargoapi.Stage{}
				require.NoError(t, c.Get(context.Background(), stageKey, liveStage))
				require.Equal(
					t,
					map[string]kargoapi.AutoPromotionHold{origin.String(): hold},
					liveStage.Status.AutoPromotionHolds,
				)
			},
		},
		{
			name:  "adds a hold",
			stage: newStage(nil),
			mutate: func(status *kargoapi.StageStatus) (bool, error) {
				status.AutoPromotionHolds = map[string]kargoapi.AutoPromotionHold{origin.String(): hold}
				return true, nil
			},
			assert: func(
				t *testing.T,
				c client.Client,
				patched bool,
				err error,
			) {
				require.NoError(t, err)
				require.True(t, patched)

				liveStage := &kargoapi.Stage{}
				require.NoError(t, c.Get(context.Background(), stageKey, liveStage))
				require.Equal(
					t,
					map[string]kargoapi.AutoPromotionHold{origin.String(): hold},
					liveStage.Status.AutoPromotionHolds,
				)
			},
		},
		{
			name:  "normalizes an empty holds map to nil",
			stage: newStage(map[string]kargoapi.AutoPromotionHold{origin.String(): hold}),
			mutate: func(status *kargoapi.StageStatus) (bool, error) {
				// Deliberately leave an empty, non-nil map behind.
				delete(status.AutoPromotionHolds, origin.String())
				return true, nil
			},
			assert: func(
				t *testing.T,
				c client.Client,
				patched bool,
				err error,
			) {
				require.NoError(t, err)
				require.True(t, patched)

				liveStage := &kargoapi.Stage{}
				require.NoError(t, c.Get(context.Background(), stageKey, liveStage))
				require.Empty(t, liveStage.Status.AutoPromotionHolds)
			},
		},
		{
			name:  "retries on conflict",
			stage: newStage(nil),
			interceptor: func() interceptor.Funcs {
				conflicts := 1
				return interceptor.Funcs{
					SubResourcePatch: func(
						ctx context.Context,
						c client.Client,
						subResourceName string,
						obj client.Object,
						patch client.Patch,
						opts ...client.SubResourcePatchOption,
					) error {
						if conflicts > 0 {
							conflicts--
							return apierrors.NewConflict(
								kargoapi.GroupVersion.WithResource("stages").GroupResource(),
								obj.GetName(),
								errors.New("simulated conflict"),
							)
						}
						return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
					},
				}
			}(),
			mutate: func(status *kargoapi.StageStatus) (bool, error) {
				status.AutoPromotionHolds = map[string]kargoapi.AutoPromotionHold{origin.String(): hold}
				return true, nil
			},
			assert: func(
				t *testing.T,
				c client.Client,
				patched bool,
				err error,
			) {
				require.NoError(t, err)
				require.True(t, patched)

				liveStage := &kargoapi.Stage{}
				require.NoError(t, c.Get(context.Background(), stageKey, liveStage))
				require.Equal(
					t,
					map[string]kargoapi.AutoPromotionHold{origin.String(): hold},
					liveStage.Status.AutoPromotionHolds,
				)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kargoapi.Stage{}).
				WithInterceptorFuncs(testCase.interceptor)
			if testCase.stage != nil {
				builder = builder.WithObjects(testCase.stage)
			}
			c := builder.Build()
			patched, err := PatchStageAutoPromotionHolds(
				t.Context(),
				c,
				c,
				stageKey,
				testCase.mutate,
			)
			testCase.assert(t, c, patched, err)
		})
	}
}

func TestClearAutoPromotionHoldAnnotation(t *testing.T) {
	now := metav1.Now().Rfc3339Copy()
	origin := kargoapi.FreightOrigin{
		Kind: kargoapi.FreightOriginKindWarehouse,
		Name: "fake-warehouse",
	}
	newHold := func() kargoapi.AutoPromotionHold {
		return kargoapi.AutoPromotionHold{
			FreightName:   "fake-freight",
			Origin:        origin,
			State:         kargoapi.AutoPromotionHoldStateActive,
			PromotionName: "fake-rollback",
			PromotionUID:  "fake-uid",
			CreatedAt:     &now,
		}
	}
	// The clearing Promotion is created after the hold.
	newPromo := func() *kargoapi.Promotion {
		return &kargoapi.Promotion{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "fake-promotion",
				Namespace:         "fake-project",
				CreationTimestamp: metav1.NewTime(now.Add(time.Minute)),
			},
		}
	}

	testCases := []struct {
		name string
		// annotated is the hold snapshotted onto the Promotion.
		annotated kargoapi.AutoPromotionHold
		// live mutates the hold compared against the annotation at clear time.
		live  func(*kargoapi.AutoPromotionHold)
		promo func(*kargoapi.Promotion)
		want  bool
	}{
		{
			name:      "round-trip matches",
			annotated: newHold(),
			want:      true,
		},
		{
			name: "nil-CreatedAt hold round-trips",
			annotated: func() kargoapi.AutoPromotionHold {
				hold := newHold()
				hold.CreatedAt = nil
				return hold
			}(),
			live: func(hold *kargoapi.AutoPromotionHold) {
				hold.CreatedAt = nil
			},
			want: true,
		},
		{
			name:      "live hold is not Active",
			annotated: newHold(),
			live: func(hold *kargoapi.AutoPromotionHold) {
				hold.State = kargoapi.AutoPromotionHoldStatePending
			},
		},
		{
			name:      "different origin",
			annotated: newHold(),
			live: func(hold *kargoapi.AutoPromotionHold) {
				hold.Origin.Name = "other-warehouse"
			},
		},
		{
			name:      "different Promotion name",
			annotated: newHold(),
			live: func(hold *kargoapi.AutoPromotionHold) {
				hold.PromotionName = "other-rollback"
			},
		},
		{
			name: "empty Promotion names never match",
			annotated: func() kargoapi.AutoPromotionHold {
				hold := newHold()
				hold.PromotionName = ""
				return hold
			}(),
			live: func(hold *kargoapi.AutoPromotionHold) {
				hold.PromotionName = ""
			},
		},
		{
			name:      "different Promotion UID",
			annotated: newHold(),
			live: func(hold *kargoapi.AutoPromotionHold) {
				hold.PromotionUID = "other-uid"
			},
		},
		{
			name:      "different CreatedAt",
			annotated: newHold(),
			live: func(hold *kargoapi.AutoPromotionHold) {
				createdAt := metav1.NewTime(now.Add(-time.Hour))
				hold.CreatedAt = &createdAt
			},
		},
		{
			name:      "nil CreatedAt on the live hold only",
			annotated: newHold(),
			live: func(hold *kargoapi.AutoPromotionHold) {
				hold.CreatedAt = nil
			},
		},
		{
			name:      "Promotion created before the hold",
			annotated: newHold(),
			promo: func(promo *kargoapi.Promotion) {
				promo.CreationTimestamp = metav1.NewTime(now.Add(-time.Minute))
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			promo := newPromo()
			SetClearAutoPromotionHoldAnnotation(promo, testCase.annotated)
			if testCase.promo != nil {
				testCase.promo(promo)
			}
			live := newHold()
			if testCase.live != nil {
				testCase.live(&live)
			}
			require.Equal(t, testCase.want, HoldMatchesClearAnnotation(live, promo))
		})
	}

	t.Run("no annotation never matches", func(t *testing.T) {
		require.False(t, HoldMatchesClearAnnotation(newHold(), newPromo()))
	})
}

func TestClearAutoPromotionHoldRequestFromPromotion(t *testing.T) {
	now := metav1.Now().Rfc3339Copy()
	origin := kargoapi.FreightOrigin{
		Kind: kargoapi.FreightOriginKindWarehouse,
		Name: "fake-warehouse",
	}

	testCases := []struct {
		name     string
		promo    *kargoapi.Promotion
		expected *kargoapi.ClearAutoPromotionHoldRequest
	}{
		{
			name:  "no annotation",
			promo: &kargoapi.Promotion{},
		},
		{
			name: "malformed payload",
			promo: &kargoapi.Promotion{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						kargoapi.AnnotationKeyClearAutoPromotionHold: "not json",
					},
				},
			},
		},
		{
			name: "valid payload",
			promo: func() *kargoapi.Promotion {
				promo := &kargoapi.Promotion{}
				SetClearAutoPromotionHoldAnnotation(promo, kargoapi.AutoPromotionHold{
					Origin:        origin,
					PromotionName: "fake-rollback",
					PromotionUID:  "fake-uid",
					CreatedAt:     &now,
				})
				return promo
			}(),
			expected: &kargoapi.ClearAutoPromotionHoldRequest{
				Origin:        origin,
				PromotionName: "fake-rollback",
				PromotionUID:  "fake-uid",
				CreatedAt:     &now,
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(
				t,
				testCase.expected,
				ClearAutoPromotionHoldRequestFromPromotion(testCase.promo),
			)
		})
	}
}
