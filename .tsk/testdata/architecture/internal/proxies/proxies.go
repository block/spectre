package proxies

import "example.com/architecture/internal/protocols/http" // want `only the root package and tests may import example.com/architecture/internal/protocols/http`

var _ = http.NewProvider
