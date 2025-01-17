// Copyright 2022 The Bucketeer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes/any"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	accountclientmock "github.com/bucketeer-io/bucketeer/pkg/account/client/mock"
	"github.com/bucketeer-io/bucketeer/pkg/cache"
	cachev3mock "github.com/bucketeer-io/bucketeer/pkg/cache/v3/mock"
	featureclientmock "github.com/bucketeer-io/bucketeer/pkg/feature/client/mock"
	featuredomain "github.com/bucketeer-io/bucketeer/pkg/feature/domain"
	ftsmock "github.com/bucketeer-io/bucketeer/pkg/feature/storage/mock"
	"github.com/bucketeer-io/bucketeer/pkg/log"
	"github.com/bucketeer-io/bucketeer/pkg/metrics"
	publishermock "github.com/bucketeer-io/bucketeer/pkg/pubsub/publisher/mock"
	"github.com/bucketeer-io/bucketeer/pkg/uuid"
	accountproto "github.com/bucketeer-io/bucketeer/proto/account"
	eventproto "github.com/bucketeer-io/bucketeer/proto/event/client"
	featureproto "github.com/bucketeer-io/bucketeer/proto/feature"
	gwproto "github.com/bucketeer-io/bucketeer/proto/gateway"
	userproto "github.com/bucketeer-io/bucketeer/proto/user"
)

func TestWithAPIKeyMemoryCacheTTL(t *testing.T) {
	t.Parallel()
	dur := time.Second
	f := WithAPIKeyMemoryCacheTTL(dur)
	opt := &options{}
	f(opt)
	assert.Equal(t, dur, opt.apiKeyMemoryCacheTTL)
}

func TestWithAPIKeyMemoryCacheEvictionInterval(t *testing.T) {
	t.Parallel()
	dur := time.Second
	f := WithAPIKeyMemoryCacheEvictionInterval(dur)
	opt := &options{}
	f(opt)
	assert.Equal(t, dur, opt.apiKeyMemoryCacheEvictionInterval)
}

func TestWithMetrics(t *testing.T) {
	t.Parallel()
	metrics := metrics.NewMetrics(
		9999,
		"/metrics",
	)
	reg := metrics.DefaultRegisterer()
	f := WithMetrics(reg)
	opt := &options{}
	f(opt)
	assert.Equal(t, reg, opt.metrics)
}

func TestWithLogger(t *testing.T) {
	t.Parallel()
	logger, err := log.NewLogger()
	require.NoError(t, err)
	f := WithLogger(logger)
	opt := &options{}
	f(opt)
	assert.Equal(t, logger, opt.logger)
}

func TestNewGrpcGatewayService(t *testing.T) {
	t.Parallel()
	g := NewGrpcGatewayService(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	assert.IsType(t, &grpcGatewayService{}, g)
}

func TestGrpcExtractAPIKeyID(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	testcases := []struct {
		ctx    context.Context
		key    string
		failed bool
	}{
		{
			ctx:    metadata.NewIncomingContext(context.TODO(), metadata.MD{}),
			key:    "",
			failed: true,
		},
		{
			ctx: metadata.NewIncomingContext(context.TODO(), metadata.MD{
				"authorization": []string{},
			}),
			key:    "",
			failed: true,
		},
		{
			ctx: metadata.NewIncomingContext(context.TODO(), metadata.MD{
				"authorization": []string{""},
			}),
			key:    "",
			failed: true,
		},
		{
			ctx: metadata.NewIncomingContext(context.TODO(), metadata.MD{
				"authorization": []string{"test-key"},
			}),
			key:    "test-key",
			failed: false,
		},
	}
	for i, tc := range testcases {
		des := fmt.Sprintf("index %d", i)
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		key, err := gs.extractAPIKeyID(tc.ctx)
		assert.Equal(t, tc.key, key, des)
		assert.Equal(t, tc.failed, err != nil, des)
	}
}

func TestGrpcGetEnvironmentAPIKey(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*grpcGatewayService)
		ctx         context.Context
		expected    *accountproto.EnvironmentAPIKey
		expectedErr error
	}{
		"exists in redis": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey:               &accountproto.APIKey{Id: "id-0"},
					}, nil)
			},
			ctx: metadata.NewIncomingContext(context.TODO(), metadata.MD{
				"authorization": []string{"test-key"},
			}),
			expected: &accountproto.EnvironmentAPIKey{
				EnvironmentNamespace: "ns0",
				ApiKey:               &accountproto.APIKey{Id: "id-0"},
			},
			expectedErr: nil,
		},
		"ErrInvalidAPIKey": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					nil, cache.ErrNotFound)
				gs.accountClient.(*accountclientmock.MockClient).EXPECT().GetAPIKeyBySearchingAllEnvironments(gomock.Any(), gomock.Any()).Return(
					nil, status.Errorf(codes.NotFound, "test"))
			},
			ctx: metadata.NewIncomingContext(context.TODO(), metadata.MD{
				"authorization": []string{"test-key"},
			}),
			expected:    nil,
			expectedErr: ErrInvalidAPIKey,
		},
		"ErrInternal": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					nil, cache.ErrNotFound)
				gs.accountClient.(*accountclientmock.MockClient).EXPECT().GetAPIKeyBySearchingAllEnvironments(gomock.Any(), gomock.Any()).Return(
					nil, status.Errorf(codes.Unknown, "test"))
			},
			ctx: metadata.NewIncomingContext(context.TODO(), metadata.MD{
				"authorization": []string{"test-key"},
			}),
			expected:    nil,
			expectedErr: ErrInternal,
		},
		"success": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					nil, cache.ErrNotFound)
				gs.accountClient.(*accountclientmock.MockClient).EXPECT().GetAPIKeyBySearchingAllEnvironments(gomock.Any(), gomock.Any()).Return(
					&accountproto.GetAPIKeyBySearchingAllEnvironmentsResponse{EnvironmentApiKey: &accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey:               &accountproto.APIKey{Id: "id-0"},
					}}, nil)
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Put(gomock.Any()).Return(nil)
			},
			ctx: metadata.NewIncomingContext(context.TODO(), metadata.MD{
				"authorization": []string{"test-key"},
			}),
			expected: &accountproto.EnvironmentAPIKey{
				EnvironmentNamespace: "ns0",
				ApiKey:               &accountproto.APIKey{Id: "id-0"},
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		p.setup(gs)
		actual, err := gs.getEnvironmentAPIKey(p.ctx)
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrpcGetEnvironmentAPIKeyFromCache(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*cachev3mock.MockEnvironmentAPIKeyCache)
		expected    *accountproto.EnvironmentAPIKey
		expectedErr error
	}{
		"no error": {
			setup: func(mtf *cachev3mock.MockEnvironmentAPIKeyCache) {
				mtf.EXPECT().Get(gomock.Any()).Return(&accountproto.EnvironmentAPIKey{}, nil)
			},
			expected:    &accountproto.EnvironmentAPIKey{},
			expectedErr: nil,
		},
		"error": {
			setup: func(mtf *cachev3mock.MockEnvironmentAPIKeyCache) {
				mtf.EXPECT().Get(gomock.Any()).Return(nil, cache.ErrNotFound)
			},
			expected:    nil,
			expectedErr: cache.ErrNotFound,
		},
	}
	for msg, p := range patterns {
		mock := cachev3mock.NewMockEnvironmentAPIKeyCache(mockController)
		p.setup(mock)
		actual, err := getEnvironmentAPIKeyFromCache(context.Background(), "id", mock, "caller", "layer")
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrpcCheckEnvironmentAPIKey(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		inputEnvAPIKey *accountproto.EnvironmentAPIKey
		inputRole      accountproto.APIKey_Role
		expected       error
	}{
		"ErrBadRole": {
			inputEnvAPIKey: &accountproto.EnvironmentAPIKey{
				EnvironmentNamespace: "ns0",
				ApiKey: &accountproto.APIKey{
					Id:       "id-0",
					Role:     accountproto.APIKey_SERVICE,
					Disabled: false,
				},
			},
			inputRole: accountproto.APIKey_SDK,
			expected:  ErrBadRole,
		},
		"ErrDisabledAPIKey: environment disabled": {
			inputEnvAPIKey: &accountproto.EnvironmentAPIKey{
				EnvironmentNamespace: "ns0",
				ApiKey: &accountproto.APIKey{
					Id:       "id-0",
					Role:     accountproto.APIKey_SDK,
					Disabled: false,
				},
				EnvironmentDisabled: true,
			},
			inputRole: accountproto.APIKey_SDK,
			expected:  ErrDisabledAPIKey,
		},
		"ErrDisabledAPIKey: api key disabled": {
			inputEnvAPIKey: &accountproto.EnvironmentAPIKey{
				EnvironmentNamespace: "ns0",
				ApiKey: &accountproto.APIKey{
					Id:       "id-0",
					Role:     accountproto.APIKey_SDK,
					Disabled: true,
				},
				EnvironmentDisabled: false,
			},
			inputRole: accountproto.APIKey_SDK,
			expected:  ErrDisabledAPIKey,
		},
		"no error": {
			inputEnvAPIKey: &accountproto.EnvironmentAPIKey{
				EnvironmentNamespace: "ns0",
				ApiKey: &accountproto.APIKey{
					Id:       "id-0",
					Role:     accountproto.APIKey_SDK,
					Disabled: false,
				},
			},
			inputRole: accountproto.APIKey_SDK,
			expected:  nil,
		},
	}
	for msg, p := range patterns {
		actual := checkEnvironmentAPIKey(p.inputEnvAPIKey, p.inputRole)
		assert.Equal(t, p.expected, actual, "%s", msg)
	}
}

func TestGrpcValidateGetEvaluationsRequest(t *testing.T) {
	t.Parallel()
	patterns := map[string]struct {
		input    *gwproto.GetEvaluationsRequest
		expected error
	}{
		"tag is empty": {
			input:    &gwproto.GetEvaluationsRequest{},
			expected: ErrTagRequired,
		},
		"user is empty": {
			input:    &gwproto.GetEvaluationsRequest{Tag: "test"},
			expected: ErrUserRequired,
		},
		"user ID is empty": {
			input:    &gwproto.GetEvaluationsRequest{Tag: "test", User: &userproto.User{}},
			expected: ErrUserIDRequired,
		},
		"pass": {
			input: &gwproto.GetEvaluationsRequest{Tag: "test", User: &userproto.User{Id: "id"}},
		},
	}
	gs := grpcGatewayService{}
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			actual := gs.validateGetEvaluationsRequest(p.input)
			assert.Equal(t, p.expected, actual)
		})
	}
}

func TestGrpcValidateGetEvaluationRequest(t *testing.T) {
	t.Parallel()
	patterns := map[string]struct {
		input    *gwproto.GetEvaluationRequest
		expected error
	}{
		"tag is empty": {
			input:    &gwproto.GetEvaluationRequest{},
			expected: ErrTagRequired,
		},
		"user is empty": {
			input:    &gwproto.GetEvaluationRequest{Tag: "test"},
			expected: ErrUserRequired,
		},
		"user ID is empty": {
			input:    &gwproto.GetEvaluationRequest{Tag: "test", User: &userproto.User{}},
			expected: ErrUserIDRequired,
		},
		"feature ID is empty": {
			input:    &gwproto.GetEvaluationRequest{Tag: "test", User: &userproto.User{Id: "id"}},
			expected: ErrFeatureIDRequired,
		},
		"pass": {
			input: &gwproto.GetEvaluationRequest{Tag: "test", User: &userproto.User{Id: "id"}, FeatureId: "id"},
		},
	}
	gs := grpcGatewayService{}
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			actual := gs.validateGetEvaluationRequest(p.input)
			assert.Equal(t, p.expected, actual)
		})
	}
}

func TestGrpcGetFeaturesFromCache(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup                func(*cachev3mock.MockFeaturesCache)
		environmentNamespace string
		expected             *featureproto.Features
		expectedErr          error
	}{
		"no error": {
			setup: func(mtf *cachev3mock.MockFeaturesCache) {
				mtf.EXPECT().Get(gomock.Any()).Return(&featureproto.Features{}, nil)
			},
			environmentNamespace: "ns0",
			expected:             &featureproto.Features{},
			expectedErr:          nil,
		},
		"error": {
			setup: func(mtf *cachev3mock.MockFeaturesCache) {
				mtf.EXPECT().Get(gomock.Any()).Return(nil, cache.ErrNotFound)
			},
			environmentNamespace: "ns0",
			expected:             nil,
			expectedErr:          cache.ErrNotFound,
		},
	}
	for msg, p := range patterns {
		mtfc := cachev3mock.NewMockFeaturesCache(mockController)
		p.setup(mtfc)
		gs := grpcGatewayService{featuresCache: mtfc}
		actual, err := gs.getFeaturesFromCache(context.Background(), p.environmentNamespace)
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrpcGetFeatures(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup                func(*grpcGatewayService)
		environmentNamespace string
		expected             []*featureproto.Feature
		expectedErr          error
	}{
		"exists in redis": {
			setup: func(gs *grpcGatewayService) {
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{{}},
					}, nil)
			},
			environmentNamespace: "ns0",
			expectedErr:          nil,
			expected:             []*featureproto.Feature{{}},
		},
		"listFeatures fails": {
			setup: func(gs *grpcGatewayService) {
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					nil, cache.ErrNotFound)
				gs.featureClient.(*featureclientmock.MockClient).EXPECT().ListFeatures(gomock.Any(), gomock.Any()).Return(
					nil, errors.New("test"))
			},
			environmentNamespace: "ns0",
			expected:             nil,
			expectedErr:          ErrInternal,
		},
		"success": {
			setup: func(gs *grpcGatewayService) {
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					nil, cache.ErrNotFound)
				gs.featureClient.(*featureclientmock.MockClient).EXPECT().ListFeatures(gomock.Any(), gomock.Any()).Return(
					&featureproto.ListFeaturesResponse{Features: []*featureproto.Feature{
						{
							Id:      "id-0",
							Enabled: true,
						},
					}}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Put(gomock.Any(), gomock.Any()).Return(nil)
			},
			environmentNamespace: "ns0",
			expected: []*featureproto.Feature{
				{
					Id:      "id-0",
					Enabled: true,
				},
			},
			expectedErr: nil,
		},
		// TODO: add test for off-variation features
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		p.setup(gs)
		actual, err := gs.getFeatures(context.Background(), p.environmentNamespace)
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrpcGetEvaluationsContextCanceled(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()
	patterns := map[string]struct {
		cancel      bool
		expected    *gwproto.GetEvaluationsResponse
		expectedErr error
	}{
		"error: context canceled": {
			cancel:      true,
			expected:    nil,
			expectedErr: ErrContextCanceled,
		},
		"error: missing API key": {
			cancel:      false,
			expected:    nil,
			expectedErr: ErrMissingAPIKey,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		ctx, cancel := context.WithCancel(context.Background())
		if p.cancel {
			cancel()
		} else {
			defer cancel()
		}
		actual, err := gs.GetEvaluations(ctx, &gwproto.GetEvaluationsRequest{})
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrpcGetEvaluationsValidation(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*grpcGatewayService)
		input       *gwproto.GetEvaluationsRequest
		expected    *gwproto.GetEvaluationsResponse
		expectedErr error
	}{
		"missing tag": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
			},
			input:       &gwproto.GetEvaluationsRequest{User: &userproto.User{Id: "id-0"}},
			expected:    nil,
			expectedErr: ErrTagRequired,
		},
		"missing user id": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
			},
			input:       &gwproto.GetEvaluationsRequest{Tag: "test"},
			expected:    nil,
			expectedErr: ErrUserRequired,
		},
		"success": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{},
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{Tag: "test", User: &userproto.User{Id: "id-0"}},
			expected: &gwproto.GetEvaluationsResponse{
				State:       featureproto.UserEvaluations_FULL,
				Evaluations: nil,
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		p.setup(gs)
		ctx := metadata.NewIncomingContext(context.TODO(), metadata.MD{
			"authorization": []string{"test-key"},
		})
		actual, err := gs.GetEvaluations(ctx, p.input)
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrpcGetEvaluationsZeroFeature(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*grpcGatewayService)
		input       *gwproto.GetEvaluationsRequest
		expected    *gwproto.GetEvaluationsResponse
		expectedErr error
	}{
		"zero feature": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{},
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{Tag: "test", User: &userproto.User{Id: "id-0"}},
			expected: &gwproto.GetEvaluationsResponse{
				State:       featureproto.UserEvaluations_FULL,
				Evaluations: nil,
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		p.setup(gs)
		ctx := metadata.NewIncomingContext(context.TODO(), metadata.MD{
			"authorization": []string{"test-key"},
		})
		actual, err := gs.GetEvaluations(ctx, p.input)
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expected.State, actual.State, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
		assert.Empty(t, actual.UserEvaluationsId, "%s", msg)
	}
}

func TestGrpcGetEvaluationsUserEvaluationsID(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	vID1 := newUUID(t)
	vID2 := newUUID(t)
	vID3 := newUUID(t)
	vID4 := newUUID(t)
	vID5 := newUUID(t)
	vID6 := newUUID(t)

	features := []*featureproto.Feature{
		{
			Id: newUUID(t),
			Variations: []*featureproto.Variation{
				{
					Id:    vID1,
					Value: "true",
				},
				{
					Id:    newUUID(t),
					Value: "false",
				},
			},
			DefaultStrategy: &featureproto.Strategy{
				Type: featureproto.Strategy_FIXED,
				FixedStrategy: &featureproto.FixedStrategy{
					Variation: vID1,
				},
			},
			Tags: []string{"android"},
		},
		{
			Id: newUUID(t),
			Variations: []*featureproto.Variation{
				{
					Id:    newUUID(t),
					Value: "true",
				},
				{
					Id:    vID2,
					Value: "false",
				},
			},
			DefaultStrategy: &featureproto.Strategy{
				Type: featureproto.Strategy_FIXED,
				FixedStrategy: &featureproto.FixedStrategy{
					Variation: vID2,
				},
			},
			Tags: []string{"android"},
		},
	}

	features2 := []*featureproto.Feature{
		{
			Id: newUUID(t),
			Variations: []*featureproto.Variation{
				{
					Id:    vID3,
					Value: "true",
				},
				{
					Id:    newUUID(t),
					Value: "false",
				},
			},
			DefaultStrategy: &featureproto.Strategy{
				Type: featureproto.Strategy_FIXED,
				FixedStrategy: &featureproto.FixedStrategy{
					Variation: vID3,
				},
			},
			Tags: []string{"ios"},
		},
		{
			Id: newUUID(t),
			Variations: []*featureproto.Variation{
				{
					Id:    newUUID(t),
					Value: "true",
				},
				{
					Id:    vID4,
					Value: "false",
				},
			},
			DefaultStrategy: &featureproto.Strategy{
				Type: featureproto.Strategy_FIXED,
				FixedStrategy: &featureproto.FixedStrategy{
					Variation: vID4,
				},
			},
			Tags: []string{"ios"},
		},
	}

	features3 := []*featureproto.Feature{
		{
			Id: newUUID(t),
			Variations: []*featureproto.Variation{
				{
					Id:    vID5,
					Value: "true",
				},
				{
					Id:    newUUID(t),
					Value: "false",
				},
			},
			DefaultStrategy: &featureproto.Strategy{
				Type: featureproto.Strategy_FIXED,
				FixedStrategy: &featureproto.FixedStrategy{
					Variation: vID5,
				},
			},
			Tags: []string{"web"},
		},
		{
			Id: newUUID(t),
			Variations: []*featureproto.Variation{
				{
					Id:    newUUID(t),
					Value: "true",
				},
				{
					Id:    vID6,
					Value: "false",
				},
			},
			DefaultStrategy: &featureproto.Strategy{
				Type: featureproto.Strategy_FIXED,
				FixedStrategy: &featureproto.FixedStrategy{
					Variation: vID6,
				},
			},
			Tags: []string{"web"},
		},
	}
	multiFeatures := append(features, features2...)
	multiFeatures = append(multiFeatures, features3...)
	userID := "user-id-0"
	userMetadata := map[string]string{"b": "value-b", "c": "value-c", "a": "value-a", "d": "value-d"}
	ueid := featuredomain.UserEvaluationsID(userID, nil, features)
	ueidWithData := featuredomain.UserEvaluationsID(userID, userMetadata, features)

	patterns := map[string]struct {
		setup                     func(*grpcGatewayService)
		input                     *gwproto.GetEvaluationsRequest
		expected                  *gwproto.GetEvaluationsResponse
		expectedErr               error
		expectedEvaluationsAssert func(assert.TestingT, interface{}, ...interface{}) bool
	}{
		"user evaluations id not set": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: features,
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{
				Tag: "test",
				User: &userproto.User{
					Id:   userID,
					Data: userMetadata,
				},
			},
			expected: &gwproto.GetEvaluationsResponse{
				State:             featureproto.UserEvaluations_FULL,
				UserEvaluationsId: ueidWithData,
			},
			expectedErr:               nil,
			expectedEvaluationsAssert: assert.NotNil,
		},
		"user evaluations id is the same": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: multiFeatures,
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{
				Tag: "test",
				User: &userproto.User{
					Id:   userID,
					Data: userMetadata,
				},
				UserEvaluationsId: featuredomain.UserEvaluationsID(userID, userMetadata, multiFeatures),
			},
			expected: &gwproto.GetEvaluationsResponse{
				State:             featureproto.UserEvaluations_FULL,
				UserEvaluationsId: featuredomain.UserEvaluationsID(userID, userMetadata, multiFeatures),
			},
			expectedErr:               nil,
			expectedEvaluationsAssert: assert.Nil,
		},
		"user evaluations id is different": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: features,
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{
				Tag: "test",
				User: &userproto.User{
					Id:   userID,
					Data: userMetadata,
				},
				UserEvaluationsId: "evaluation-id",
			},
			expected: &gwproto.GetEvaluationsResponse{
				State:             featureproto.UserEvaluations_FULL,
				UserEvaluationsId: ueidWithData,
			},
			expectedErr:               nil,
			expectedEvaluationsAssert: assert.NotNil,
		},
		"user_with_no_metadata_and_the_id_is_same": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: features,
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{
				Tag:               "test",
				User:              &userproto.User{Id: userID},
				UserEvaluationsId: ueid,
			},
			expected: &gwproto.GetEvaluationsResponse{
				State:             featureproto.UserEvaluations_FULL,
				UserEvaluationsId: ueid,
			},
			expectedErr:               nil,
			expectedEvaluationsAssert: assert.Nil,
		},
		"user_with_no_metadata_and_the_id_is_different": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: features,
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{
				Tag:               "test",
				User:              &userproto.User{Id: userID},
				UserEvaluationsId: "evaluation-id",
			},
			expected: &gwproto.GetEvaluationsResponse{
				State:             featureproto.UserEvaluations_FULL,
				UserEvaluationsId: ueid,
			},
			expectedErr:               nil,
			expectedEvaluationsAssert: assert.NotNil,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		p.setup(gs)
		ctx := metadata.NewIncomingContext(context.TODO(), metadata.MD{
			"authorization": []string{"test-key"},
		})
		actual, err := gs.GetEvaluations(ctx, p.input)
		assert.Equal(t, p.expected.State, actual.State, "%s", msg)
		assert.Equal(t, p.expected.UserEvaluationsId, actual.UserEvaluationsId, "%s", msg)
		p.expectedEvaluationsAssert(t, actual.Evaluations, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrpcGetEvaluationsNoSegmentList(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()
	vID1 := newUUID(t)
	vID2 := newUUID(t)
	vID3 := newUUID(t)
	vID4 := newUUID(t)

	patterns := map[string]struct {
		setup       func(*grpcGatewayService)
		input       *gwproto.GetEvaluationsRequest
		expected    *gwproto.GetEvaluationsResponse
		expectedErr error
	}{
		"state: full": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{
							{
								Id: "feature-a",
								Variations: []*featureproto.Variation{
									{
										Id:    vID1,
										Value: "true",
									},
									{
										Id:    newUUID(t),
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: vID1,
									},
								},
								Tags: []string{"android"},
							},
							{
								Id: "feature-b",
								Variations: []*featureproto.Variation{
									{
										Id:    newUUID(t),
										Value: "true",
									},
									{
										Id:    vID2,
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: vID2,
									},
								},
								Tags: []string{"android"},
							},
							{
								Id: "feature-c",
								Variations: []*featureproto.Variation{
									{
										Id:    vID3,
										Value: "true",
									},
									{
										Id:    newUUID(t),
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: vID3,
									},
								},
								Tags: []string{"ios"},
							},
							{
								Id: "feature-d",
								Variations: []*featureproto.Variation{
									{
										Id:    newUUID(t),
										Value: "true",
									},
									{
										Id:    vID4,
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: vID4,
									},
								},
								Tags: []string{"ios"},
							},
						},
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{Tag: "ios", User: &userproto.User{Id: "id-0"}},
			expected: &gwproto.GetEvaluationsResponse{
				State: featureproto.UserEvaluations_FULL,
				Evaluations: &featureproto.UserEvaluations{
					Evaluations: []*featureproto.Evaluation{
						{
							VariationId: vID3,
						},
						{
							VariationId: vID4,
						},
					},
				},
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		p.setup(gs)
		ctx := metadata.NewIncomingContext(context.TODO(), metadata.MD{
			"authorization": []string{"test-key"},
		})
		actual, err := gs.GetEvaluations(ctx, p.input)
		ev := p.expected.Evaluations.Evaluations
		av := actual.Evaluations.Evaluations
		assert.Equal(t, len(ev), len(av), "%s", msg)
		assert.Equal(t, p.expected.State, actual.State, "%s", msg)
		assert.Equal(t, ev[0].VariationId, av[0].VariationId, "%s", msg)
		assert.Equal(t, ev[1].VariationId, av[1].VariationId, "%s", msg)
		assert.NotEmpty(t, actual.UserEvaluationsId, "%s", msg)
		require.NoError(t, err)
	}
}

func TestGrpcGetEvaluationsEvaluteFeatures(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*grpcGatewayService)
		input       *gwproto.GetEvaluationsRequest
		expected    *gwproto.GetEvaluationsResponse
		expectedErr error
	}{
		"errInternal": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{
							{
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								Rules: []*featureproto.Rule{
									{
										Id: "rule-1",
										Strategy: &featureproto.Strategy{
											Type: featureproto.Strategy_FIXED,
											FixedStrategy: &featureproto.FixedStrategy{
												Variation: "variation-b",
											},
										},
										Clauses: []*featureproto.Clause{
											{
												Id:        "clause-1",
												Attribute: "name",
												Operator:  featureproto.Clause_SEGMENT,
												Values: []string{
													"id-0",
												},
											},
										},
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
						},
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.segmentUsersCache.(*cachev3mock.MockSegmentUsersCache).EXPECT().Get(gomock.Any(), gomock.Any()).Return(
					nil, errors.New("random error"))
				gs.featureClient.(*featureclientmock.MockClient).EXPECT().ListSegmentUsers(gomock.Any(), gomock.Any()).Return(
					nil, ErrInternal)
			},
			input:       &gwproto.GetEvaluationsRequest{Tag: "test", User: &userproto.User{Id: "id-0"}},
			expected:    nil,
			expectedErr: ErrInternal,
		},
		"state: full, evaluate features list segment from cache": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{

						Features: []*featureproto.Feature{
							{
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								Rules: []*featureproto.Rule{
									{
										Id: "rule-1",
										Strategy: &featureproto.Strategy{
											Type: featureproto.Strategy_FIXED,
											FixedStrategy: &featureproto.FixedStrategy{
												Variation: "variation-b",
											},
										},
										Clauses: []*featureproto.Clause{
											{
												Id:        "clause-1",
												Attribute: "name",
												Operator:  featureproto.Clause_SEGMENT,
												Values: []string{
													"id-0",
												},
											},
										},
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-a",
									},
								},
								Tags: []string{"test"},
							},
						},
					}, nil)
				gs.segmentUsersCache.(*cachev3mock.MockSegmentUsersCache).EXPECT().Get(gomock.Any(), gomock.Any()).Return(
					&featureproto.SegmentUsers{
						SegmentId: "segment-id",
						Users: []*featureproto.SegmentUser{
							{
								SegmentId: "segment-id",
								UserId:    "user-id-1",
								State:     featureproto.SegmentUser_INCLUDED,
								Deleted:   false,
							},
							{
								SegmentId: "segment-id",
								UserId:    "user-id-2",
								State:     featureproto.SegmentUser_INCLUDED,
								Deleted:   false,
							},
						},
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{Tag: "test", User: &userproto.User{Id: "id-0"}},
			expected: &gwproto.GetEvaluationsResponse{
				State: featureproto.UserEvaluations_FULL,
				Evaluations: &featureproto.UserEvaluations{
					Evaluations: []*featureproto.Evaluation{
						{
							VariationId: "variation-b",
							Reason: &featureproto.Reason{
								Type: featureproto.Reason_DEFAULT,
							},
						},
					},
				},
			},
			expectedErr: nil,
		},
		"state: full, evaluate features list segment from storage": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{
							{
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								Rules: []*featureproto.Rule{
									{
										Id: "rule-1",
										Strategy: &featureproto.Strategy{
											Type: featureproto.Strategy_FIXED,
											FixedStrategy: &featureproto.FixedStrategy{
												Variation: "variation-b",
											},
										},
										Clauses: []*featureproto.Clause{
											{
												Id:        "clause-1",
												Attribute: "name",
												Operator:  featureproto.Clause_SEGMENT,
												Values: []string{
													"id-0",
												},
											},
										},
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
						},
					}, nil)
				gs.segmentUsersCache.(*cachev3mock.MockSegmentUsersCache).EXPECT().Get(gomock.Any(), gomock.Any()).Return(
					nil, errors.New("random error"))
				gs.segmentUsersCache.(*cachev3mock.MockSegmentUsersCache).EXPECT().Put(gomock.Any(), gomock.Any()).Return(nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.featureClient.(*featureclientmock.MockClient).EXPECT().ListSegmentUsers(gomock.Any(), gomock.Any()).Return(
					&featureproto.ListSegmentUsersResponse{}, nil)
			},
			input: &gwproto.GetEvaluationsRequest{Tag: "test", User: &userproto.User{Id: "id-0"}},
			expected: &gwproto.GetEvaluationsResponse{
				State: featureproto.UserEvaluations_FULL,
				Evaluations: &featureproto.UserEvaluations{
					Evaluations: []*featureproto.Evaluation{
						{
							VariationId: "variation-b",
							Reason: &featureproto.Reason{
								Type: featureproto.Reason_DEFAULT,
							},
						},
					},
				},
			},
			expectedErr: nil,
		},
		"state: full, evaluate features": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{
							{
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
						},
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
			},
			input: &gwproto.GetEvaluationsRequest{Tag: "test", User: &userproto.User{Id: "id-0"}},
			expected: &gwproto.GetEvaluationsResponse{
				State: featureproto.UserEvaluations_FULL,
				Evaluations: &featureproto.UserEvaluations{
					Evaluations: []*featureproto.Evaluation{
						{
							VariationId: "variation-b",
							Reason: &featureproto.Reason{
								Type: featureproto.Reason_DEFAULT,
							},
						},
					},
				},
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		p.setup(gs)
		ctx := metadata.NewIncomingContext(context.TODO(), metadata.MD{
			"authorization": []string{"test-key"},
		})
		actual, err := gs.GetEvaluations(ctx, p.input)
		if err != nil {
			assert.Equal(t, p.expected, actual, "%s", msg)
			assert.Equal(t, p.expectedErr, err, "%s", msg)
		} else {
			assert.Equal(t, len(p.expected.Evaluations.Evaluations), 1, "%s", msg)
			assert.Equal(t, p.expected.State, actual.State, "%s", msg)
			assert.Equal(t, p.expected.Evaluations.Evaluations[0].VariationId, "variation-b", "%s", msg)
			assert.Equal(t, p.expected.Evaluations.Evaluations[0].Reason, actual.Evaluations.Evaluations[0].Reason, msg)
			assert.NotEmpty(t, actual.UserEvaluationsId, "%s", msg)
			require.NoError(t, err)
		}
	}
}

func TestGrpcGetEvaluation(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup             func(*grpcGatewayService)
		input             *gwproto.GetEvaluationRequest
		expectedFeatureID string
		expectedErr       error
	}{
		"errFeatureNotFound": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{
							{
								Id: "feature-id-1",
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
							{
								Id: "feature-id-2",
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-c",
										Value: "true",
									},
									{
										Id:    "variation-d",
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-d",
									},
								},
								Tags: []string{"test"},
							},
						},
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
			},
			input:             &gwproto.GetEvaluationRequest{Tag: "test", User: &userproto.User{Id: "id-0"}, FeatureId: "feature-id-3"},
			expectedFeatureID: "",
			expectedErr:       ErrFeatureNotFound,
		},
		"errInternal": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{
							{
								Id: "feature-id-1",
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
							{
								Id: "feature-id-2",
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-c",
										Value: "true",
									},
									{
										Id:    "variation-d",
										Value: "false",
									},
								},
								Rules: []*featureproto.Rule{
									{
										Id: "rule-1",
										Strategy: &featureproto.Strategy{
											Type: featureproto.Strategy_FIXED,
											FixedStrategy: &featureproto.FixedStrategy{
												Variation: "variation-b",
											},
										},
										Clauses: []*featureproto.Clause{
											{
												Id:        "clause-1",
												Attribute: "name",
												Operator:  featureproto.Clause_SEGMENT,
												Values: []string{
													"id-0",
												},
											},
										},
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-d",
									},
								},
								Tags: []string{"test"},
							},
						},
					}, nil)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.segmentUsersCache.(*cachev3mock.MockSegmentUsersCache).EXPECT().Get(gomock.Any(), gomock.Any()).Return(
					nil, errors.New("random error"))
				gs.featureClient.(*featureclientmock.MockClient).EXPECT().ListSegmentUsers(gomock.Any(), gomock.Any()).Return(
					nil, ErrInternal)
			},
			input:             &gwproto.GetEvaluationRequest{Tag: "test", User: &userproto.User{Id: "id-0"}, FeatureId: "feature-id-2"},
			expectedFeatureID: "",
			expectedErr:       ErrInternal,
		},
		"error while trying to upsert the user evaluation": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{
							{
								Id: "feature-id-1",
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
							{
								Id: "feature-id-2",
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
						},
					}, nil)
				gs.userEvaluationStorage.(*ftsmock.MockUserEvaluationsStorage).EXPECT().UpsertUserEvaluation(
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
				).Return(errors.New("storage: internal")).MaxTimes(1)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
			},
			input:             &gwproto.GetEvaluationRequest{Tag: "test", User: &userproto.User{Id: "id-0"}, FeatureId: "feature-id-2"},
			expectedFeatureID: "",
			expectedErr:       ErrInternal,
		},
		"return evaluation": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.featuresCache.(*cachev3mock.MockFeaturesCache).EXPECT().Get(gomock.Any()).Return(
					&featureproto.Features{
						Features: []*featureproto.Feature{
							{
								Id: "feature-id-1",
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
							{
								Id: "feature-id-2",
								Variations: []*featureproto.Variation{
									{
										Id:    "variation-a",
										Value: "true",
									},
									{
										Id:    "variation-b",
										Value: "false",
									},
								},
								DefaultStrategy: &featureproto.Strategy{
									Type: featureproto.Strategy_FIXED,
									FixedStrategy: &featureproto.FixedStrategy{
										Variation: "variation-b",
									},
								},
								Tags: []string{"test"},
							},
						},
					}, nil)
				gs.userEvaluationStorage.(*ftsmock.MockUserEvaluationsStorage).EXPECT().UpsertUserEvaluation(
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
				).Return(nil).MaxTimes(1)
				gs.userPublisher.(*publishermock.MockPublisher).EXPECT().Publish(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
			},
			input:             &gwproto.GetEvaluationRequest{Tag: "test", User: &userproto.User{Id: "id-0"}, FeatureId: "feature-id-2"},
			expectedFeatureID: "feature-id-2",
			expectedErr:       nil,
		},
	}
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			gs := newGrpcGatewayServiceWithMock(t, mockController)
			p.setup(gs)
			ctx := metadata.NewIncomingContext(context.TODO(), metadata.MD{
				"authorization": []string{"test-key"},
			})
			actual, err := gs.GetEvaluation(ctx, p.input)
			assert.Equal(t, p.expectedErr, err)
			if err == nil {
				assert.Equal(t, p.expectedFeatureID, actual.Evaluation.FeatureId)
			}
		})
	}
}

func TestGrpcRegisterEventsContextCanceled(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()
	patterns := map[string]struct {
		cancel      bool
		expected    *gwproto.RegisterEventsResponse
		expectedErr error
	}{
		"error: context canceled": {
			cancel:      true,
			expected:    nil,
			expectedErr: ErrContextCanceled,
		},
		"error: missing API key": {
			cancel:      false,
			expected:    nil,
			expectedErr: ErrMissingAPIKey,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		ctx, cancel := context.WithCancel(context.Background())
		if p.cancel {
			cancel()
		} else {
			defer cancel()
		}
		actual, err := gs.RegisterEvents(ctx, &gwproto.RegisterEventsRequest{})
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrcpRegisterEvents(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	bGoalEvent, err := proto.Marshal(&eventproto.GoalEvent{Timestamp: time.Now().Unix()})
	if err != nil {
		t.Fatal("could not serialize goal event")
	}
	bGoalBatchEvent, err := proto.Marshal(&eventproto.GoalBatchEvent{
		UserId: "0efe416e-2fd2-4996-b5c3-194f05444f1f",
		UserGoalEventsOverTags: []*eventproto.UserGoalEventsOverTag{
			{
				Tag: "tag",
			},
		},
	})
	if err != nil {
		t.Fatal("could not serialize goal batch event")
	}
	bEvaluationEvent, err := proto.Marshal(&eventproto.EvaluationEvent{Timestamp: time.Now().Unix()})
	if err != nil {
		t.Fatal("could not serialize evaluation event")
	}
	bInvalidEvent, err := proto.Marshal(&any.Any{})
	if err != nil {
		t.Fatal("could not serialize experiment event")
	}
	bMetricsEvent, err := proto.Marshal(&eventproto.MetricsEvent{Timestamp: time.Now().Unix()})
	if err != nil {
		t.Fatal("could not serialize metrics event")
	}
	uuid0 := newUUID(t)
	uuid1 := newUUID(t)
	uuid2 := newUUID(t)
	uuid3 := newUUID(t)

	patterns := map[string]struct {
		setup       func(*grpcGatewayService)
		input       *gwproto.RegisterEventsRequest
		expected    *gwproto.RegisterEventsResponse
		expectedErr error
	}{
		"error: zero event": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
			},
			input:       &gwproto.RegisterEventsRequest{},
			expectedErr: ErrMissingEvents,
		},
		"error: ErrMissingEventID": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
			},
			input: &gwproto.RegisterEventsRequest{
				Events: []*eventproto.Event{
					{
						Id: "",
					},
				},
			},
			expectedErr: ErrMissingEventID,
		},
		"error: invalid message type": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.goalPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.goalBatchPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.evaluationPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.metricsPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
			},
			input: &gwproto.RegisterEventsRequest{
				Events: []*eventproto.Event{
					{
						Id: uuid0,
						Event: &any.Any{
							TypeUrl: "github.com/golang/protobuf/ptypes/any",
							Value:   bInvalidEvent,
						},
					},
				},
			},
			expected: &gwproto.RegisterEventsResponse{
				Errors: map[string]*gwproto.RegisterEventsResponse_Error{
					uuid0: {
						Retriable: false,
						Message:   "Invalid message type",
					},
				},
			},
			expectedErr: nil,
		},
		"error while trying to upsert the user evaluation": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.goalPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.goalBatchPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.evaluationPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.metricsPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.userEvaluationStorage.(*ftsmock.MockUserEvaluationsStorage).EXPECT().UpsertUserEvaluation(
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
				).Return(errors.New("storage: internal")).MaxTimes(1)
			},
			input: &gwproto.RegisterEventsRequest{
				Events: []*eventproto.Event{
					{
						Id: uuid0,
						Event: &any.Any{
							TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.GoalEvent",
							Value:   bGoalEvent,
						},
					},
					{
						Id: uuid1,
						Event: &any.Any{
							TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.EvaluationEvent",
							Value:   bEvaluationEvent,
						},
					},
					{
						Id: uuid2,
						Event: &any.Any{
							TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.MetricsEvent",
							Value:   bMetricsEvent,
						},
					},
					{
						Id: uuid3,
						Event: &any.Any{
							TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.GoalBatchEvent",
							Value:   bGoalBatchEvent,
						},
					},
				},
			},
			expected: &gwproto.RegisterEventsResponse{
				Errors: map[string]*gwproto.RegisterEventsResponse_Error{
					uuid1: {
						Retriable: true,
						Message:   "Failed to upsert user evaluation",
					},
				},
			},
			expectedErr: nil,
		},
		"success": {
			setup: func(gs *grpcGatewayService) {
				gs.environmentAPIKeyCache.(*cachev3mock.MockEnvironmentAPIKeyCache).EXPECT().Get(gomock.Any()).Return(
					&accountproto.EnvironmentAPIKey{
						EnvironmentNamespace: "ns0",
						ApiKey: &accountproto.APIKey{
							Id:       "id-0",
							Role:     accountproto.APIKey_SDK,
							Disabled: false,
						},
					}, nil)
				gs.goalPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.goalBatchPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.evaluationPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.metricsPublisher.(*publishermock.MockPublisher).EXPECT().PublishMulti(gomock.Any(), gomock.Any()).Return(
					nil).MaxTimes(1)
				gs.userEvaluationStorage.(*ftsmock.MockUserEvaluationsStorage).EXPECT().UpsertUserEvaluation(
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
				).Return(nil).MaxTimes(1)
			},
			input: &gwproto.RegisterEventsRequest{
				Events: []*eventproto.Event{
					{
						Id: uuid0,
						Event: &any.Any{
							TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.GoalEvent",
							Value:   bGoalEvent,
						},
					},
					{
						Id: uuid1,
						Event: &any.Any{
							TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.EvaluationEvent",
							Value:   bEvaluationEvent,
						},
					},
					{
						Id: uuid2,
						Event: &any.Any{
							TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.MetricsEvent",
							Value:   bMetricsEvent,
						},
					},
					{
						Id: uuid3,
						Event: &any.Any{
							TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.GoalBatchEvent",
							Value:   bGoalBatchEvent,
						},
					},
				},
			},
			expected:    &gwproto.RegisterEventsResponse{Errors: make(map[string]*gwproto.RegisterEventsResponse_Error)},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		p.setup(gs)
		ctx := metadata.NewIncomingContext(context.TODO(), metadata.MD{
			"authorization": []string{"test-key"},
		})
		actual, err := gs.RegisterEvents(ctx, p.input)
		assert.Equal(t, p.expected, actual, "%s", msg)
		assert.Equal(t, p.expectedErr, err, "%s", msg)
	}
}

func TestGrpcConvToEvaluation(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()
	tag := "tag"
	evaluationEvent := &eventproto.EvaluationEvent{
		FeatureId:      "feature-id",
		FeatureVersion: 2,
		UserId:         "user-id",
		VariationId:    "variation-id",
		User:           &userproto.User{Id: "user-id"},
		Reason: &featureproto.Reason{
			Type: featureproto.Reason_DEFAULT,
		},
		Tag:       tag,
		Timestamp: time.Now().Unix(),
	}
	bEvaluationEventWithTag, err := proto.Marshal(evaluationEvent)
	evaluationEvent.Tag = ""
	bEvaluationEventWithoutTag, err := proto.Marshal(evaluationEvent)
	assert.NoError(t, err)
	bInvalidEvent, err := proto.Marshal(&any.Any{})
	assert.NoError(t, err)

	patterns := []struct {
		desc        string
		input       *eventproto.Event
		expected    *featureproto.Evaluation
		expectedTag string
		expectedErr error
	}{
		{
			desc: "error",
			input: &eventproto.Event{
				Id: "id",
				Event: &any.Any{
					TypeUrl: "github.com/golang/protobuf/ptypes/any",
					Value:   bInvalidEvent,
				},
			},
			expected:    nil,
			expectedTag: "",
			expectedErr: errUnmarshalFailed,
		},
		{
			desc: "success without tag",
			input: &eventproto.Event{
				Id: "id",
				Event: &any.Any{
					TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.EvaluationEvent",
					Value:   bEvaluationEventWithoutTag,
				},
			},
			expected: &featureproto.Evaluation{
				Id: featuredomain.EvaluationID(
					evaluationEvent.FeatureId,
					evaluationEvent.FeatureVersion,
					evaluationEvent.UserId,
				),
				FeatureId:      evaluationEvent.FeatureId,
				FeatureVersion: evaluationEvent.FeatureVersion,
				UserId:         evaluationEvent.UserId,
				VariationId:    evaluationEvent.VariationId,
				Reason:         evaluationEvent.Reason,
			},
			expectedTag: "none",
			expectedErr: nil,
		},
		{
			desc: "success with tag",
			input: &eventproto.Event{
				Id: "id",
				Event: &any.Any{
					TypeUrl: "github.com/bucketeer-io/bucketeer/proto/event/client/bucketeer.event.client.EvaluationEvent",
					Value:   bEvaluationEventWithTag,
				},
			},
			expected: &featureproto.Evaluation{
				Id: featuredomain.EvaluationID(
					evaluationEvent.FeatureId,
					evaluationEvent.FeatureVersion,
					evaluationEvent.UserId,
				),
				FeatureId:      evaluationEvent.FeatureId,
				FeatureVersion: evaluationEvent.FeatureVersion,
				UserId:         evaluationEvent.UserId,
				VariationId:    evaluationEvent.VariationId,
				Reason:         evaluationEvent.Reason,
			},
			expectedTag: tag,
			expectedErr: nil,
		},
	}
	for _, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		ev, tag, err := gs.convToEvaluation(context.Background(), p.input)
		assert.True(t, proto.Equal(p.expected, ev), p.desc)
		assert.Equal(t, p.expectedTag, tag, p.desc)
		assert.Equal(t, p.expectedErr, err, p.desc)
	}
}

func TestGrpcContainsInvalidTimestampError(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()
	patterns := map[string]struct {
		errs     map[string]*gwproto.RegisterEventsResponse_Error
		expected bool
	}{
		"error: invalid timestamp": {
			errs: map[string]*gwproto.RegisterEventsResponse_Error{
				"id-test": {
					Retriable: false,
					Message:   errInvalidTimestamp.Error(),
				},
			},
			expected: true,
		},
		"error: different error": {
			errs: map[string]*gwproto.RegisterEventsResponse_Error{
				"id-test": {
					Retriable: true,
					Message:   errUnmarshalFailed.Error(),
				},
			},
			expected: false,
		},
		"error: empty": {
			errs:     make(map[string]*gwproto.RegisterEventsResponse_Error),
			expected: false,
		},
	}
	for msg, p := range patterns {
		gs := newGrpcGatewayServiceWithMock(t, mockController)
		actual := gs.containsInvalidTimestampError(p.errs)
		assert.Equal(t, p.expected, actual, "%s", msg)
	}
}

func newGrpcGatewayServiceWithMock(t *testing.T, mockController *gomock.Controller) *grpcGatewayService {
	logger, err := log.NewLogger()
	require.NoError(t, err)
	return &grpcGatewayService{
		userEvaluationStorage:  ftsmock.NewMockUserEvaluationsStorage(mockController),
		featureClient:          featureclientmock.NewMockClient(mockController),
		accountClient:          accountclientmock.NewMockClient(mockController),
		goalPublisher:          publishermock.NewMockPublisher(mockController),
		goalBatchPublisher:     publishermock.NewMockPublisher(mockController),
		userPublisher:          publishermock.NewMockPublisher(mockController),
		metricsPublisher:       publishermock.NewMockPublisher(mockController),
		evaluationPublisher:    publishermock.NewMockPublisher(mockController),
		featuresCache:          cachev3mock.NewMockFeaturesCache(mockController),
		segmentUsersCache:      cachev3mock.NewMockSegmentUsersCache(mockController),
		environmentAPIKeyCache: cachev3mock.NewMockEnvironmentAPIKeyCache(mockController),
		opts:                   &defaultOptions,
		logger:                 logger,
	}
}

func newUUID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}
