// Package script is the HTTP protocol's part of the SPECTRE script API: the ingress and egress
// members that declare HTTP rules and endpoints, the callbacks that apply them, and the payload types.
package script

import "github.com/block/spectre/internal/model"

// Protocol is the protocol of HTTP servers, including gRPC and Connect.
const Protocol model.Protocol = "http"

// ConnectError is the body of a failed Connect unary response.
const ConnectError = "spectre.connect.Error"
