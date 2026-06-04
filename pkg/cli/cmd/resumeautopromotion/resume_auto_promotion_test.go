package resumeautopromotion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptionsValidate(t *testing.T) {
	testCases := []struct {
		name       string
		opts       options
		assertions func(*testing.T, options, error)
	}{
		{
			name: "valid input",
			opts: options{
				Project: "fake-project",
				Stage:   "fake-stage",
				Origin:  "Warehouse/fake-warehouse",
			},
			assertions: func(t *testing.T, _ options, err error) {
				require.NoError(t, err)
			},
		},
		{
			name: "trims surrounding whitespace",
			opts: options{
				Project: "  fake-project  ",
				Stage:   "  fake-stage  ",
				Origin:  "  Warehouse/fake-warehouse  ",
			},
			assertions: func(t *testing.T, opts options, err error) {
				require.NoError(t, err)
				assert.Equal(t, "fake-project", opts.Project)
				assert.Equal(t, "fake-stage", opts.Stage)
				assert.Equal(t, "Warehouse/fake-warehouse", opts.Origin)
			},
		},
		{
			name: "missing project",
			opts: options{
				Stage:  "fake-stage",
				Origin: "Warehouse/fake-warehouse",
			},
			assertions: func(t *testing.T, _ options, err error) {
				require.ErrorContains(t, err, "project is required")
			},
		},
		{
			name: "missing stage",
			opts: options{
				Project: "fake-project",
				Origin:  "Warehouse/fake-warehouse",
			},
			assertions: func(t *testing.T, _ options, err error) {
				require.ErrorContains(t, err, "stage is required")
			},
		},
		{
			name: "missing origin",
			opts: options{
				Project: "fake-project",
				Stage:   "fake-stage",
			},
			assertions: func(t *testing.T, _ options, err error) {
				require.ErrorContains(t, err, "origin is required")
			},
		},
		{
			name: "origin missing kind/name separator",
			opts: options{
				Project: "fake-project",
				Stage:   "fake-stage",
				Origin:  "fake-warehouse",
			},
			assertions: func(t *testing.T, _ options, err error) {
				require.ErrorContains(t, err, "invalid origin")
			},
		},
		{
			name: "origin with unsupported kind",
			opts: options{
				Project: "fake-project",
				Stage:   "fake-stage",
				Origin:  "Stage/fake-stage",
			},
			assertions: func(t *testing.T, _ options, err error) {
				require.ErrorContains(t, err, "invalid origin")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.opts.validate()
			tc.assertions(t, tc.opts, err)
		})
	}
}
