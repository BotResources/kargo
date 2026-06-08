package promote

import (
	"errors"
	"testing"

	"github.com/go-openapi/runtime"
	"github.com/stretchr/testify/require"

	"github.com/akuity/kargo/pkg/client/generated/core"
	"github.com/akuity/kargo/pkg/client/generated/models"
)

func TestPromotionOptionsValidate(t *testing.T) {
	testCases := []struct {
		name       string
		opts       promotionOptions
		assertions func(*testing.T, promotionOptions, error)
	}{
		{
			name: "accepts reason for direct stage promotion",
			opts: promotionOptions{
				Project:     "fake-project",
				FreightName: "fake-freight",
				Stage:       "fake-stage",
				Reason:      " rollback ",
			},
			assertions: func(t *testing.T, opts promotionOptions, err error) {
				require.NoError(t, err)
				require.Equal(t, "rollback", opts.Reason)
			},
		},
		{
			name: "rejects reason for downstream promotion",
			opts: promotionOptions{
				Project:        "fake-project",
				FreightName:    "fake-freight",
				DownstreamFrom: "fake-stage",
				Reason:         "rollback",
			},
			assertions: func(t *testing.T, _ promotionOptions, err error) {
				require.ErrorContains(t, err, "reason can only be used")
			},
		},
		{
			name: "rejects reason for abort",
			opts: promotionOptions{
				Project:   "fake-project",
				Promotion: "fake-promotion",
				Abort:     true,
				Reason:    "rollback",
			},
			assertions: func(t *testing.T, _ promotionOptions, err error) {
				require.ErrorContains(t, err, "reason can only be used")
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.opts.validate()
			testCase.assertions(t, testCase.opts, err)
		})
	}
}

func TestPromotionOptionsExpectedAutoPromotionCandidate(t *testing.T) {
	const (
		project = "fake-project"
		stage   = "fake-stage"
	)

	otherOriginName := "other-warehouse"

	testCases := []struct {
		name       string
		opts       promotionOptions
		client     fakeAutoPromotionCandidateClient
		assertions func(*testing.T, string, error)
	}{
		{
			name: "matches selected freight origin",
			opts: promotionOptions{
				Project:     project,
				Stage:       stage,
				FreightName: "selected-freight",
			},
			client: fakeAutoPromotionCandidateClient{
				freight: selectedFreight(),
				candidates: []*models.AutoPromotionCandidate{
					candidateWithOrigin("other-candidate", otherOriginName),
					candidateWithOrigin("expected-candidate", selectedOriginName),
				},
			},
			assertions: func(t *testing.T, candidate string, err error) {
				require.NoError(t, err)
				require.Equal(t, "expected-candidate", candidate)
			},
		},
		{
			name: "uses alias to resolve selected freight",
			opts: promotionOptions{
				Project:      project,
				Stage:        stage,
				FreightAlias: "selected-alias",
			},
			client: fakeAutoPromotionCandidateClient{
				freight: selectedFreight(),
				candidates: []*models.AutoPromotionCandidate{
					candidateWithOrigin("expected-candidate", selectedOriginName),
				},
			},
			assertions: func(t *testing.T, candidate string, err error) {
				require.NoError(t, err)
				require.Equal(t, "expected-candidate", candidate)
			},
		},
		{
			name: "returns empty when no candidate matches origin",
			opts: promotionOptions{
				Project:     project,
				Stage:       stage,
				FreightName: "selected-freight",
			},
			client: fakeAutoPromotionCandidateClient{
				freight: selectedFreight(),
				candidates: []*models.AutoPromotionCandidate{
					candidateWithOrigin("other-candidate", otherOriginName),
				},
			},
			assertions: func(t *testing.T, candidate string, err error) {
				require.NoError(t, err)
				require.Empty(t, candidate)
			},
		},
		{
			name: "formats freight lookup error",
			opts: promotionOptions{
				Project:     project,
				Stage:       stage,
				FreightName: "selected-freight",
			},
			client: fakeAutoPromotionCandidateClient{
				getFreightErr: errors.New("boom"),
			},
			assertions: func(t *testing.T, _ string, err error) {
				require.ErrorContains(t, err, "get freight: boom")
			},
		},
		{
			name: "formats candidate lookup error",
			opts: promotionOptions{
				Project:     project,
				Stage:       stage,
				FreightName: "selected-freight",
			},
			client: fakeAutoPromotionCandidateClient{
				freight:          selectedFreight(),
				getCandidatesErr: errors.New("boom"),
			},
			assertions: func(t *testing.T, _ string, err error) {
				require.ErrorContains(t, err, "get auto-promotion candidates: boom")
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate, err := testCase.opts.expectedAutoPromotionCandidate(
				t.Context(),
				&testCase.client,
			)
			testCase.assertions(t, candidate, err)
		})
	}
}

type fakeAutoPromotionCandidateClient struct {
	freight          *models.Freight
	candidates       []*models.AutoPromotionCandidate
	getFreightErr    error
	getCandidatesErr error
}

func (f *fakeAutoPromotionCandidateClient) GetFreight(
	*core.GetFreightParams,
	runtime.ClientAuthInfoWriter,
	...core.ClientOption,
) (*core.GetFreightOK, error) {
	if f.getFreightErr != nil {
		return nil, f.getFreightErr
	}
	return &core.GetFreightOK{Payload: f.freight}, nil
}

func (f *fakeAutoPromotionCandidateClient) GetStageAutoPromotionCandidates(
	*core.GetStageAutoPromotionCandidatesParams,
	runtime.ClientAuthInfoWriter,
	...core.ClientOption,
) (*core.GetStageAutoPromotionCandidatesOK, error) {
	if f.getCandidatesErr != nil {
		return nil, f.getCandidatesErr
	}
	return &core.GetStageAutoPromotionCandidatesOK{
		Payload: &models.AutoPromotionCandidatesResponse{
			Candidates: f.candidates,
		},
	}, nil
}

const selectedOriginName = "selected-warehouse"

func selectedFreight() *models.Freight {
	kind := "Warehouse"
	freight := &models.Freight{}
	freight.Origin.Kind = &kind
	freight.Origin.Name = ptrTo(selectedOriginName)
	return freight
}

func candidateWithOrigin(
	freightName string,
	name string,
) *models.AutoPromotionCandidate {
	kind := "Warehouse"
	return &models.AutoPromotionCandidate{
		FreightName: freightName,
		Origin:      &models.FreightOrigin{Kind: &kind, Name: &name},
	}
}

func ptrTo[T any](v T) *T {
	return &v
}
