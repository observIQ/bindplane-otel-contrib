module github.com/observiq/bindplane-otel-contrib/internal/blobstream

go 1.26.4

require (
	github.com/bodgit/sevenzip v1.6.5
	github.com/gabriel-vasile/mimetype v1.4.15
	github.com/klauspost/compress v1.19.2
	github.com/linkedin/goavro/v2 v2.15.0
	github.com/nwaples/rardecode/v2 v2.3.0
	github.com/observiq/bindplane-otel-contrib/internal/storageclient v1.13.0
	github.com/pierrec/lz4/v4 v4.1.29
	github.com/sorairolake/lzip-go v0.3.8
	github.com/stretchr/testify v1.12.1
	github.com/ulikunitz/xz v0.5.16
	go.opentelemetry.io/collector/pdata v1.65.0
	go.uber.org/zap v1.28.0
)

require (
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/golang/snappy v0.0.1 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/stangelandcl/ppmd v0.1.1 // indirect
	go.opentelemetry.io/collector/component v1.65.0 // indirect
	go.opentelemetry.io/collector/extension v1.65.0 // indirect
	go.opentelemetry.io/collector/extension/xextension v0.159.0 // indirect
	go.opentelemetry.io/collector/featuregate v1.65.0 // indirect
	go.opentelemetry.io/collector/internal/componentalias v0.159.0 // indirect
	go.opentelemetry.io/collector/pipeline v1.65.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	go4.org v0.0.0-20260112195520-a5071408f32f // indirect
	golang.org/x/text v0.40.0 // indirect
)

replace github.com/observiq/bindplane-otel-contrib/internal/storageclient => ../storageclient
