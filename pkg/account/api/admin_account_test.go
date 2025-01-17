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
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	v2as "github.com/bucketeer-io/bucketeer/pkg/account/storage/v2"
	ecmock "github.com/bucketeer-io/bucketeer/pkg/environment/client/mock"
	"github.com/bucketeer-io/bucketeer/pkg/locale"
	storagemock "github.com/bucketeer-io/bucketeer/pkg/storage/mock"
	"github.com/bucketeer-io/bucketeer/pkg/storage/v2/mysql"
	mysqlmock "github.com/bucketeer-io/bucketeer/pkg/storage/v2/mysql/mock"
	accountproto "github.com/bucketeer-io/bucketeer/proto/account"
	environmentproto "github.com/bucketeer-io/bucketeer/proto/environment"
)

func TestGetMeMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		ctx             context.Context
		setup           func(*AccountService)
		input           *accountproto.GetMeRequest
		expected        string
		expectedIsAdmin bool
		expectedErr     error
	}{
		"errUnauthenticated": {
			ctx:         context.Background(),
			setup:       nil,
			input:       &accountproto.GetMeRequest{},
			expected:    "",
			expectedErr: localizedError(statusUnauthenticated, locale.JaJP),
		},
		"errInvalidEmail": {
			ctx:         createContextWithInvalidEmailToken(t, accountproto.Account_OWNER),
			setup:       nil,
			input:       &accountproto.GetMeRequest{},
			expected:    "",
			expectedErr: localizedError(statusInvalidEmail, locale.JaJP),
		},
		"errInternal": {
			ctx: createContextWithDefaultToken(t, accountproto.Account_OWNER),
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListProjects(
					gomock.Any(),
					gomock.Any(),
				).Return(
					nil,
					localizedError(statusInternal, locale.JaJP),
				)
			},
			input:       &accountproto.GetMeRequest{},
			expected:    "",
			expectedErr: localizedError(statusInternal, locale.JaJP),
		},
		"errInternal_no_projects": {
			ctx: createContextWithDefaultToken(t, accountproto.Account_OWNER),
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListProjects(
					gomock.Any(),
					gomock.Any(),
				).Return(
					&environmentproto.ListProjectsResponse{},
					nil,
				)
			},
			input:       &accountproto.GetMeRequest{},
			expected:    "",
			expectedErr: localizedError(statusInternal, locale.JaJP),
		},
		"errInternal_no_environments": {
			ctx: createContextWithDefaultToken(t, accountproto.Account_OWNER),
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListProjects(
					gomock.Any(),
					gomock.Any(),
				).Return(
					&environmentproto.ListProjectsResponse{
						Projects: getProjects(t),
						Cursor:   "",
					},
					nil,
				)
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListEnvironments(
					gomock.Any(),
					gomock.Any(),
				).Return(
					&environmentproto.ListEnvironmentsResponse{},
					nil,
				)
			},
			input:       &accountproto.GetMeRequest{},
			expected:    "",
			expectedErr: localizedError(statusInternal, locale.JaJP),
		},
		"errNotFound": {
			ctx: createContextWithDefaultToken(t, accountproto.Account_EDITOR),
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListProjects(
					gomock.Any(),
					gomock.Any(),
				).Return(
					&environmentproto.ListProjectsResponse{
						Projects: getProjects(t),
						Cursor:   "",
					},
					nil,
				)
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListEnvironments(
					gomock.Any(),
					gomock.Any(),
				).Return(
					&environmentproto.ListEnvironmentsResponse{
						Environments: getEnvironments(t),
						Cursor:       "",
					},
					nil,
				)
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(mysql.ErrNoRows).Times(3)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row).Times(3)
			},
			input:       &accountproto.GetMeRequest{},
			expected:    "",
			expectedErr: localizedError(statusNotFound, locale.JaJP),
		},
	}
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			service := createAccountService(t, mockController, nil)
			if p.setup != nil {
				p.setup(service)
			}
			actual, err := service.GetMe(p.ctx, p.input)
			assert.Equal(t, p.expectedErr, err, msg)
			if actual != nil {
				assert.Equal(t, p.expected, actual.Email, msg)
				assert.Equal(t, p.expectedIsAdmin, actual.IsAdmin, msg)
			}
		})
	}
}

func TestCreateAdminAccountMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		ctxRole     accountproto.Account_Role
		req         *accountproto.CreateAdminAccountRequest
		expectedErr error
	}{
		"errNoCommand": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAdminAccountRequest{
				Command: nil,
			},
			expectedErr: localizedError(statusNoCommand, locale.JaJP),
		},
		"errEmailIsEmpty": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAdminAccountRequest{
				Command: &accountproto.CreateAdminAccountCommand{Email: ""},
			},
			expectedErr: localizedError(statusEmailIsEmpty, locale.JaJP),
		},
		"errInvalidEmail": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAdminAccountRequest{
				Command: &accountproto.CreateAdminAccountCommand{Email: "bucketeer@"},
			},
			expectedErr: localizedError(statusInvalidEmail, locale.JaJP),
		},
		"errInternal": {
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListEnvironments(
					gomock.Any(), gomock.Any(),
				).Return(nil, localizedError(statusInternal, locale.JaJP))
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAdminAccountRequest{
				Command: &accountproto.CreateAdminAccountCommand{
					Email: "bucketeer@example.com",
				},
			},
			expectedErr: localizedError(statusInternal, locale.JaJP),
		},
		"errAlreadyExists_EnvironmentAccount": {
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListEnvironments(
					gomock.Any(), gomock.Any(),
				).Return(&environmentproto.ListEnvironmentsResponse{
					Environments: getEnvironments(t),
					Cursor:       "",
				}, nil)
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAdminAccountRequest{
				Command: &accountproto.CreateAdminAccountCommand{
					Email: "bucketeer_environment@example.com",
				},
			},
			expectedErr: localizedError(statusAlreadyExists, locale.JaJP),
		},
		"errAlreadyExists_AdminAccount": {
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListEnvironments(
					gomock.Any(), gomock.Any(),
				).Return(&environmentproto.ListEnvironmentsResponse{
					Environments: getEnvironments(t),
					Cursor:       "",
				}, nil)
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(mysql.ErrNoRows).Times(2)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row).Times(2)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(v2as.ErrAdminAccountAlreadyExists)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAdminAccountRequest{
				Command: &accountproto.CreateAdminAccountCommand{
					Email: "bucketeer_admin@example.com",
				},
			},
			expectedErr: localizedError(statusAlreadyExists, locale.JaJP),
		},
		"success": {
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListEnvironments(
					gomock.Any(), gomock.Any(),
				).Return(&environmentproto.ListEnvironmentsResponse{
					Environments: getEnvironments(t),
					Cursor:       "",
				}, nil)
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(mysql.ErrNoRows).Times(2)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row).Times(2)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(nil)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAdminAccountRequest{
				Command: &accountproto.CreateAdminAccountCommand{
					Email: "bucketeer@example.com",
				},
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			ctx := createContextWithDefaultToken(t, p.ctxRole)
			service := createAccountService(t, mockController, storagemock.NewMockClient(mockController))
			if p.setup != nil {
				p.setup(service)
			}
			_, err := service.CreateAdminAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err, msg)
		})
	}
}

func TestEnableAdminAccountMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		ctxRole     accountproto.Account_Role
		req         *accountproto.EnableAdminAccountRequest
		expectedErr error
	}{
		"errMissingAccountID": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.EnableAdminAccountRequest{
				Id: "",
			},
			expectedErr: localizedError(statusMissingAccountID, locale.JaJP),
		},
		"errNoCommand": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.EnableAdminAccountRequest{
				Id:      "id",
				Command: nil,
			},
			expectedErr: localizedError(statusNoCommand, locale.JaJP),
		},
		"errNotFound": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(v2as.ErrAdminAccountNotFound)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.EnableAdminAccountRequest{
				Id:      "id",
				Command: &accountproto.EnableAdminAccountCommand{},
			},
			expectedErr: localizedError(statusNotFound, locale.JaJP),
		},
		"errInternal": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(errors.New("error"))
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.EnableAdminAccountRequest{
				Id:      "bucketeer@example.com",
				Command: &accountproto.EnableAdminAccountCommand{},
			},
			expectedErr: localizedError(statusInternal, locale.JaJP),
		},
		"success": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(nil)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.EnableAdminAccountRequest{
				Id:      "bucketeer@example.com",
				Command: &accountproto.EnableAdminAccountCommand{},
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			ctx := createContextWithDefaultToken(t, p.ctxRole)
			service := createAccountService(t, mockController, nil)
			if p.setup != nil {
				p.setup(service)
			}
			_, err := service.EnableAdminAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err, msg)
		})
	}
}

func TestDisableAdminAccountMySQL(t *testing.T) {
	mockController := gomock.NewController(t)
	defer mockController.Finish()
	t.Parallel()

	patterns := map[string]struct {
		setup       func(*AccountService)
		ctxRole     accountproto.Account_Role
		req         *accountproto.DisableAdminAccountRequest
		expectedErr error
	}{
		"errMissingAccountID": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.DisableAdminAccountRequest{
				Id: "",
			},
			expectedErr: localizedError(statusMissingAccountID, locale.JaJP),
		},
		"errNoCommand": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.DisableAdminAccountRequest{
				Id:      "id",
				Command: nil,
			},
			expectedErr: localizedError(statusNoCommand, locale.JaJP),
		},
		"errNotFound": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(v2as.ErrAdminAccountNotFound)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.DisableAdminAccountRequest{
				Id:      "id",
				Command: &accountproto.DisableAdminAccountCommand{},
			},
			expectedErr: localizedError(statusNotFound, locale.JaJP),
		},
		"errInternal": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(errors.New("error"))
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.DisableAdminAccountRequest{
				Id:      "bucketeer@example.com",
				Command: &accountproto.DisableAdminAccountCommand{},
			},
			expectedErr: localizedError(statusInternal, locale.JaJP),
		},
		"success": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(nil)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.DisableAdminAccountRequest{
				Id:      "bucketeer@example.com",
				Command: &accountproto.DisableAdminAccountCommand{},
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			ctx := createContextWithDefaultToken(t, p.ctxRole)
			service := createAccountService(t, mockController, nil)
			if p.setup != nil {
				p.setup(service)
			}
			_, err := service.DisableAdminAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err, msg)
		})
	}
}

func TestConvertAccountMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		ctxRole     accountproto.Account_Role
		req         *accountproto.ConvertAccountRequest
		expectedErr error
	}{
		"errMissingAccountID": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.ConvertAccountRequest{
				Id:      "",
				Command: &accountproto.ConvertAccountCommand{},
			},
			expectedErr: localizedError(statusMissingAccountID, locale.JaJP),
		},
		"errNotFound": {
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListEnvironments(
					gomock.Any(),
					gomock.Any(),
				).Return(&environmentproto.ListEnvironmentsResponse{
					Environments: getEnvironments(t),
					Cursor:       "",
				}, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(v2as.ErrAccountNotFound)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.ConvertAccountRequest{
				Id:      "b@aa.jp",
				Command: &accountproto.ConvertAccountCommand{},
			},
			expectedErr: localizedError(statusNotFound, locale.JaJP),
		},
		"success": {
			setup: func(s *AccountService) {
				s.environmentClient.(*ecmock.MockClient).EXPECT().ListEnvironments(
					gomock.Any(),
					gomock.Any(),
				).Return(&environmentproto.ListEnvironmentsResponse{
					Environments: getEnvironments(t),
					Cursor:       "",
				}, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(nil)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.ConvertAccountRequest{
				Id:      "bucketeer@example.com",
				Command: &accountproto.ConvertAccountCommand{},
			},
			expectedErr: nil,
		},
	}
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			ctx := createContextWithDefaultToken(t, p.ctxRole)
			service := createAccountService(t, mockController, storagemock.NewMockClient(mockController))
			if p.setup != nil {
				p.setup(service)
			}
			_, err := service.ConvertAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err, msg)
		})
	}
}

func TestGetAdminAccountMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		req         *accountproto.GetAdminAccountRequest
		expectedErr error
	}{
		"errMissingAccountID": {
			req: &accountproto.GetAdminAccountRequest{
				Email: "",
			},
			expectedErr: localizedError(statusEmailIsEmpty, locale.JaJP),
		},
		"errInvalidEmail": {
			req: &accountproto.GetAdminAccountRequest{
				Email: "bucketeer@",
			},
			expectedErr: localizedError(statusInvalidEmail, locale.JaJP),
		},
		"errNotFound": {
			setup: func(s *AccountService) {
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(mysql.ErrNoRows)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row)
			},
			req: &accountproto.GetAdminAccountRequest{
				Email: "service@example.com",
			},
			expectedErr: localizedError(statusNotFound, locale.JaJP),
		},
		"success": {
			setup: func(s *AccountService) {
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row)
			},
			req: &accountproto.GetAdminAccountRequest{
				Email: "bucketeer@example.com",
			},
			expectedErr: nil,
		},
	}
	ctx := createContextWithDefaultToken(t, accountproto.Account_OWNER)
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			service := createAccountService(t, mockController, nil)
			if p.setup != nil {
				p.setup(service)
			}
			res, err := service.GetAdminAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err)
			if res != nil {
				assert.NotNil(t, res)
			}
		})
	}
}

func TestListAdminAccountsMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		input       *accountproto.ListAdminAccountsRequest
		expected    *accountproto.ListAdminAccountsResponse
		expectedErr error
	}{
		"errInvalidCursor": {
			setup:       nil,
			input:       &accountproto.ListAdminAccountsRequest{Cursor: "xxx"},
			expected:    nil,
			expectedErr: localizedError(statusInvalidCursor, locale.JaJP),
		},
		"errInternal": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(nil, errors.New("test"))
			},
			input:       &accountproto.ListAdminAccountsRequest{},
			expected:    nil,
			expectedErr: localizedError(statusInternal, locale.JaJP),
		},
		"success": {
			setup: func(s *AccountService) {
				rows := mysqlmock.NewMockRows(mockController)
				rows.EXPECT().Close().Return(nil)
				rows.EXPECT().Next().Return(false)
				rows.EXPECT().Err().Return(nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(rows, nil)
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row)
			},
			input:       &accountproto.ListAdminAccountsRequest{PageSize: 2, Cursor: ""},
			expected:    &accountproto.ListAdminAccountsResponse{Accounts: []*accountproto.Account{}, Cursor: "0", TotalCount: 0},
			expectedErr: nil,
		},
	}
	ctx := createContextWithDefaultToken(t, accountproto.Account_OWNER)
	for msg, p := range patterns {
		t.Run(msg, func(t *testing.T) {
			service := createAccountService(t, mockController, nil)
			if p.setup != nil {
				p.setup(service)
			}
			actual, err := service.ListAdminAccounts(ctx, p.input)
			assert.Equal(t, p.expectedErr, err, msg)
			assert.Equal(t, p.expected, actual, msg)
		})
	}
}

func getProjects(t *testing.T) []*environmentproto.Project {
	t.Helper()
	return []*environmentproto.Project{
		{Id: "pj0"},
	}
}

func getEnvironments(t *testing.T) []*environmentproto.Environment {
	t.Helper()
	return []*environmentproto.Environment{
		{Id: "ns0", Namespace: "ns0", ProjectId: "pj0"},
		{Id: "ns1", Namespace: "ns1", ProjectId: "pj0"},
	}
}
