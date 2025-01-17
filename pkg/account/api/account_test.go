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
	"errors"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	v2as "github.com/bucketeer-io/bucketeer/pkg/account/storage/v2"
	"github.com/bucketeer-io/bucketeer/pkg/locale"
	"github.com/bucketeer-io/bucketeer/pkg/storage/v2/mysql"
	mysqlmock "github.com/bucketeer-io/bucketeer/pkg/storage/v2/mysql/mock"
	accountproto "github.com/bucketeer-io/bucketeer/proto/account"
)

func TestCreateAccountMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		ctxRole     accountproto.Account_Role
		req         *accountproto.CreateAccountRequest
		expectedErr error
	}{
		"errNoCommand": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAccountRequest{
				Command:              nil,
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusNoCommand, locale.JaJP),
		},
		"errInvalidIsEmpty": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAccountRequest{
				Command:              &accountproto.CreateAccountCommand{Email: ""},
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusEmailIsEmpty, locale.JaJP),
		},
		"errInvalidEmail": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAccountRequest{
				Command:              &accountproto.CreateAccountCommand{Email: "bucketeer@"},
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusInvalidEmail, locale.JaJP),
		},
		"errAlreadyExists_AdminAccount": {
			setup: func(s *AccountService) {
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAccountRequest{
				Command:              &accountproto.CreateAccountCommand{Email: "bucketeer_admin@example.com"},
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusAlreadyExists, locale.JaJP),
		},
		"errAlreadyExists_EnvironmentAccount": {
			setup: func(s *AccountService) {
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(mysql.ErrNoRows)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(v2as.ErrAccountAlreadyExists)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAccountRequest{
				Command:              &accountproto.CreateAccountCommand{Email: "bucketeer_environment@example.com"},
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusAlreadyExists, locale.JaJP),
		},
		"errInternal": {
			setup: func(s *AccountService) {
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(errors.New("error"))
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAccountRequest{
				Command: &accountproto.CreateAccountCommand{
					Email: "bucketeer@example.com",
					Role:  accountproto.Account_OWNER,
				},
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusInternal, locale.JaJP),
		},
		"success": {
			setup: func(s *AccountService) {
				row := mysqlmock.NewMockRow(mockController)
				row.EXPECT().Scan(gomock.Any()).Return(mysql.ErrNoRows)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryRowContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(row)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(nil)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.CreateAccountRequest{
				Command: &accountproto.CreateAccountCommand{
					Email: "bucketeer@example.com",
					Role:  accountproto.Account_OWNER,
				},
				EnvironmentNamespace: "ns0",
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
			_, err := service.CreateAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err, msg)
		})
	}
}

func TestChangeAccountRoleMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		ctxRole     accountproto.Account_Role
		req         *accountproto.ChangeAccountRoleRequest
		expectedErr error
	}{
		"errMissingAccountID": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.ChangeAccountRoleRequest{
				Id:                   "",
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusMissingAccountID, locale.JaJP),
		},
		"errNoCommand": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.ChangeAccountRoleRequest{
				Id:                   "id",
				Command:              nil,
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusNoCommand, locale.JaJP),
		},
		"errNotFound": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(v2as.ErrAccountNotFound)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.ChangeAccountRoleRequest{
				Id: "id",
				Command: &accountproto.ChangeAccountRoleCommand{
					Role: accountproto.Account_VIEWER,
				},
				EnvironmentNamespace: "ns0",
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
			req: &accountproto.ChangeAccountRoleRequest{
				Id: "bucketeer@example.com",
				Command: &accountproto.ChangeAccountRoleCommand{
					Role: accountproto.Account_VIEWER,
				},
				EnvironmentNamespace: "ns0",
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
			req: &accountproto.ChangeAccountRoleRequest{
				Id: "bucketeer@example.com",
				Command: &accountproto.ChangeAccountRoleCommand{
					Role: accountproto.Account_VIEWER,
				},
				EnvironmentNamespace: "ns0",
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
			_, err := service.ChangeAccountRole(ctx, p.req)
			assert.Equal(t, p.expectedErr, err, msg)
		})
	}
}

func TestEnableAccountMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		ctxRole     accountproto.Account_Role
		req         *accountproto.EnableAccountRequest
		expectedErr error
	}{
		"errMissingAccountID": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.EnableAccountRequest{
				Id:                   "",
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusMissingAccountID, locale.JaJP),
		},
		"errNoCommand": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.EnableAccountRequest{
				Id:                   "id",
				Command:              nil,
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusNoCommand, locale.JaJP),
		},
		"errNotFound": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(v2as.ErrAccountNotFound)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.EnableAccountRequest{
				Id:                   "id",
				Command:              &accountproto.EnableAccountCommand{},
				EnvironmentNamespace: "ns0",
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
			req: &accountproto.EnableAccountRequest{
				Id:                   "bucketeer@example.com",
				Command:              &accountproto.EnableAccountCommand{},
				EnvironmentNamespace: "ns0",
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
			req: &accountproto.EnableAccountRequest{
				Id:                   "bucketeer@example.com",
				Command:              &accountproto.EnableAccountCommand{},
				EnvironmentNamespace: "ns0",
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
			_, err := service.EnableAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err, msg)
		})
	}
}

func TestDisableAccountMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		ctxRole     accountproto.Account_Role
		req         *accountproto.DisableAccountRequest
		expectedErr error
	}{
		"errMissingAccountID": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.DisableAccountRequest{
				Id:                   "",
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusMissingAccountID, locale.JaJP),
		},
		"errNoCommand": {
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.DisableAccountRequest{
				Id:                   "id",
				Command:              nil,
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusNoCommand, locale.JaJP),
		},
		"errNotFound": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().BeginTx(gomock.Any()).Return(nil, nil)
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().RunInTransaction(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(v2as.ErrAccountNotFound)
			},
			ctxRole: accountproto.Account_OWNER,
			req: &accountproto.DisableAccountRequest{
				Id:                   "id",
				Command:              &accountproto.DisableAccountCommand{},
				EnvironmentNamespace: "ns0",
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
			req: &accountproto.DisableAccountRequest{
				Id:                   "bucketeer@example.com",
				Command:              &accountproto.DisableAccountCommand{},
				EnvironmentNamespace: "ns0",
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
			req: &accountproto.DisableAccountRequest{
				Id:                   "bucketeer@example.com",
				Command:              &accountproto.DisableAccountCommand{},
				EnvironmentNamespace: "ns0",
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
			_, err := service.DisableAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err, msg)
		})
	}
}

func TestGetAccountMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		req         *accountproto.GetAccountRequest
		expectedErr error
	}{
		"errMissingAccountID": {
			req: &accountproto.GetAccountRequest{
				Email:                "",
				EnvironmentNamespace: "ns0",
			},
			expectedErr: localizedError(statusEmailIsEmpty, locale.JaJP),
		},
		"errInvalidEmail": {
			req: &accountproto.GetAccountRequest{
				Email:                "bucketeer@",
				EnvironmentNamespace: "ns0",
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
			req: &accountproto.GetAccountRequest{
				Email:                "service@example.com",
				EnvironmentNamespace: "ns0",
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
			req: &accountproto.GetAccountRequest{
				Email:                "bucketeer@example.com",
				EnvironmentNamespace: "ns0",
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
			res, err := service.GetAccount(ctx, p.req)
			assert.Equal(t, p.expectedErr, err)
			if err == nil {
				assert.NotNil(t, res)
			}
		})
	}
}
func TestListAccountsMySQL(t *testing.T) {
	t.Parallel()
	mockController := gomock.NewController(t)
	defer mockController.Finish()

	patterns := map[string]struct {
		setup       func(*AccountService)
		input       *accountproto.ListAccountsRequest
		expected    *accountproto.ListAccountsResponse
		expectedErr error
	}{
		"errInvalidCursor": {
			setup:       nil,
			input:       &accountproto.ListAccountsRequest{EnvironmentNamespace: "ns0", Cursor: "XXX"},
			expected:    nil,
			expectedErr: localizedError(statusInvalidCursor, locale.JaJP),
		},
		"errInternal": {
			setup: func(s *AccountService) {
				s.mysqlClient.(*mysqlmock.MockClient).EXPECT().QueryContext(
					gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(nil, errors.New("test"))
			},
			input:       &accountproto.ListAccountsRequest{EnvironmentNamespace: "ns0"},
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
			input:       &accountproto.ListAccountsRequest{PageSize: 2, Cursor: "", EnvironmentNamespace: "ns0"},
			expected:    &accountproto.ListAccountsResponse{Accounts: []*accountproto.Account{}, Cursor: "0"},
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
			actual, err := service.ListAccounts(ctx, p.input)
			assert.Equal(t, p.expectedErr, err, msg)
			assert.Equal(t, p.expected, actual, msg)
		})
	}
}
