// Package sample provides a deterministic Connect service for ingress experiments.
package sample

import (
	"context"
	"slices"

	"connectrpc.com/connect"
	"github.com/alecthomas/errors"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	samplepb "github.com/block/spectre/internal/sample/pb"
)

// Service serves a read-only snapshot loaded from ProtoJSON.
type Service struct {
	// The snapshot is immutable after construction; each RPC returns an independent copy.
	snapshot *samplepb.ListUsersResponse
}

// New loads sample users from a ProtoJSON ListUsersResponse.
func New(data []byte) (*Service, error) {
	snapshot := &samplepb.ListUsersResponse{}
	if err := protojson.Unmarshal(data, snapshot); err != nil {
		return nil, errors.Wrap(err, "decode sample users")
	}
	seen := make(map[string]bool)
	for _, user := range snapshot.GetUsers() {
		if user.GetId() == "" {
			return nil, errors.New("sample user has no ID")
		}
		if seen[user.GetId()] {
			return nil, errors.New("duplicate sample user ID")
		}
		seen[user.GetId()] = true
	}
	return &Service{snapshot: snapshot}, nil
}

// GetUser returns one sample user, or InvalidArgument or NotFound for an invalid ID.
func (s *Service) GetUser(ctx context.Context, request *connect.Request[samplepb.GetUserRequest]) (*connect.Response[samplepb.GetUserResponse], error) {
	if err := ctx.Err(); err != nil {
		code := connect.CodeCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			code = connect.CodeDeadlineExceeded
		}
		return nil, connect.NewError(code, errors.Wrap(err, "get user"))
	}
	if request.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	}
	for _, user := range s.snapshot.GetUsers() {
		if user.GetId() == request.Msg.GetId() {
			return connect.NewResponse(&samplepb.GetUserResponse{User: proto.Clone(user).(*samplepb.User)}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
}

// ListUsers filters by IDs and optional role, preserving fixture order and its fixed timestamp.
func (s *Service) ListUsers(ctx context.Context, request *connect.Request[samplepb.ListUsersRequest]) (*connect.Response[samplepb.ListUsersResponse], error) {
	if err := ctx.Err(); err != nil {
		code := connect.CodeCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			code = connect.CodeDeadlineExceeded
		}
		return nil, connect.NewError(code, errors.Wrap(err, "list users"))
	}
	response := proto.Clone(s.snapshot).(*samplepb.ListUsersResponse)
	users := response.GetUsers()
	response.Users = nil
	for _, user := range users {
		if len(request.Msg.GetIds()) > 0 && !slices.Contains(request.Msg.GetIds(), user.GetId()) {
			continue
		}
		if request.Msg.Role != nil && !slices.Contains(user.GetRoles(), request.Msg.GetRole()) {
			continue
		}
		response.Users = append(response.GetUsers(), user)
	}
	response.TotalCount = int64(len(response.GetUsers()))
	return connect.NewResponse(response), nil
}
