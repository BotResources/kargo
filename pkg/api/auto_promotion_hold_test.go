package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
				var exists *AutoPromotionHoldExistsError
				require.ErrorAs(t, err, &exists)
				require.Equal(t, kargoapi.AutoPromotionHoldStateActive, exists.State)

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
				require.ErrorContains(t, err, "create rollback Promotion")

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
				require.ErrorContains(t, err, "create rollback Promotion")

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
