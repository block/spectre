// Package rpc gives schema methods their HTTP meaning of unary RPC operations,
// and checks them against protobuf descriptors.
package rpc

import "github.com/block/spectre/internal/schema"

// Operation is one unary RPC method declared on a service interface.
type Operation struct {
	// Name is the service's type name followed by the method name.
	Name string
	// Request names the request object type.
	Request schema.TypeRef
	// Response names the response object type.
	Response schema.TypeRef
}
