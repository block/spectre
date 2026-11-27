module github.com/block/spectre

go 1.27.1

// v1 was tagged by mistake from a breaking change while still on v0.
retract [v1.0.0, v1.8.1]

require (
	connectrpc.com/connect/v2 v2.0.2
	connectrpc.com/grpcreflect/v2 v2.0.0
	github.com/DataDog/sketches-go v1.4.8
	github.com/alecthomas/assert/v2 v2.11.0
	github.com/alecthomas/errors v0.9.1
	github.com/alecthomas/kong v1.17.0
	github.com/alecthomas/kong-toml v0.4.1
	github.com/alecthomas/types v0.20.1
	github.com/alicebob/miniredis/v2 v2.40.0
	github.com/block/scaffolder v1.12.0
	github.com/go-mysql-org/go-mysql v1.16.0
	github.com/go-sql-driver/mysql v1.10.2
	github.com/gobwas/glob v1.1.0
	github.com/google/nftables v0.3.0
	github.com/grafana/sobek v0.0.0-20261105103427-a5b602015248
	github.com/lmittmann/tint v1.2.1
	github.com/microsoft/TypeScript/tsc/shim/typescript v0.0.0
	github.com/pelletier/go-toml/v2 v2.4.3
	github.com/pingcap/tidb/pkg/parser v0.0.0-20260504140133-511dba1dbe17
	github.com/redis/go-redis/v9 v9.23.0
	go.jetify.com/typeid v1.3.0
	golang.org/x/mod v0.42.0
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	google.golang.org/protobuf v1.36.12
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/Microsoft/go-winio v0.6.4 // indirect
	github.com/alecthomas/repr v0.5.2 // indirect
	github.com/block/getit v0.1.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coreos/go-semver v0.3.1 // indirect
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/dlclark/regexp2/v2 v2.11.0 // indirect
	github.com/dop251/goja v0.0.0-20260311135729-065cd970411c // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/gofrs/uuid/v5 v5.2.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hexops/gotextdiff v1.0.3 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/mdlayher/netlink v1.7.3-0.20250113171957-fbb4dce95f42 // indirect
	github.com/mdlayher/socket v0.5.0 // indirect
	github.com/microsoft/TypeScript/tsc v0.0.0-20261114002809-6bdcb54d23f4 // indirect
	github.com/pelletier/go-toml v1.9.5 // indirect
	github.com/pingcap/errors v0.11.5-0.20260310054046-9c8b3586e4b2 // indirect
	github.com/pingcap/failpoint v0.0.0-20260406204437-bbc9d102c19e // indirect
	github.com/pingcap/log v1.1.1-0.20260227082333-572e590d08f1 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

// TypeScript 7 has no public Go API, and Go only lets packages under
// github.com/microsoft/TypeScript/tsc import its internal packages. This local
// module claims a path under it so it can re-export the parser and transpiler.
replace github.com/microsoft/TypeScript/tsc/shim/typescript => ./internal/typescript
