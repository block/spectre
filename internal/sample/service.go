// Package sample provides a deterministic gRPC service for ingress experiments.
package sample

import (
	"context"
	"slices"

	"github.com/alecthomas/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	samplepb "github.com/block/spectre/internal/sample/pb"
)

// Service serves a read-only snapshot loaded from ProtoJSON.
type Service struct {
	samplepb.UnimplementedUserServiceServer
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
func (s *Service) GetUser(ctx context.Context, request *samplepb.GetUserRequest) (*samplepb.GetUserResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(status.FromContextError(err).Err(), "get user")
	}
	if request.GetId() == "" {
		return nil, errors.Wrap(status.Error(codes.InvalidArgument, "id is required"), "get user")
	}
	for _, user := range s.snapshot.GetUsers() {
		if user.GetId() == request.GetId() {
			return &samplepb.GetUserResponse{User: proto.Clone(user).(*samplepb.User)}, nil
		}
	}
	return nil, errors.Wrap(status.Error(codes.NotFound, "user not found"), "get user")
}

// ListUsers filters by IDs and optional role, preserving fixture order and its fixed timestamp.
func (s *Service) ListUsers(ctx context.Context, request *samplepb.ListUsersRequest) (*samplepb.ListUsersResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(status.FromContextError(err).Err(), "list users")
	}
	response := proto.Clone(s.snapshot).(*samplepb.ListUsersResponse)
	users := response.GetUsers()
	response.Users = nil
	for _, user := range users {
		if len(request.GetIds()) > 0 && !slices.Contains(request.GetIds(), user.GetId()) {
			continue
		}
		if request.Role != nil && !slices.Contains(user.GetRoles(), request.GetRole()) {
			continue
		}
		response.Users = append(response.GetUsers(), user)
	}
	response.TotalCount = int64(len(response.GetUsers()))
	return response, nil
}
