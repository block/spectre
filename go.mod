module github.com/block/spectre

go 1.27.1

require (
	connectrpc.com/connect v1.21.0
	connectrpc.com/grpcreflect v1.3.0
	github.com/alecthomas/assert/v2 v2.11.0
	github.com/alecthomas/errors v0.9.1
	github.com/alecthomas/kong v1.16.1
	github.com/alecthomas/kong-toml v0.4.1
	github.com/alecthomas/types v0.20.1
	github.com/grafana/sobek v0.0.0-20260915160442-8a431c44cd7b
	github.com/lmittmann/tint v1.2.0
	github.com/microsoft/TypeScript/tsc/shim/typescript v0.0.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	golang.org/x/net v0.58.0
	golang.org/x/sync v0.23.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/alecthomas/repr v0.5.2 // indirect
	github.com/dlclark/regexp2/v2 v2.8.0 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	github.com/hexops/gotextdiff v1.0.3 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/microsoft/TypeScript/tsc v0.0.0-20261001235638-09b1db061731 // indirect
	github.com/pelletier/go-toml v1.9.5 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

// TypeScript 7 has no public Go API, and Go only lets packages under
// github.com/microsoft/TypeScript/tsc import its internal packages. This local
// module claims a path under it so it can re-export the parser and transpiler.
replace github.com/microsoft/TypeScript/tsc/shim/typescript => ./internal/typescript
