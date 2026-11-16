module github.com/onehumanbeing/esp32-iphone-agent/cli

go 1.26.0

require (
	github.com/anthropics/anthropic-sdk-go/v2 v2.9.0
	github.com/mozillazg/go-pinyin v0.21.0
	golang.org/x/image v0.47.0
	golang.org/x/term v0.47.0
	golang.org/x/text v0.43.0
	tinygo.org/x/bluetooth v0.16.0
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/saltosystems/winrt-go v0.0.0-20260317170058-9c2fec580d96 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/soypat/cyw43439 v0.1.2-0.20260731160358-f2a6af121857 // indirect
	github.com/soypat/lneto v0.3.2 // indirect
	github.com/soypat/seqs v0.0.0-20260125140838-2c1c6b1bd69e // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/tinygo-org/cbgo v0.0.4 // indirect
	github.com/tinygo-org/pio v0.3.0 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel v1.33.0 // indirect
	go.opentelemetry.io/otel/metric v1.33.0 // indirect
	go.opentelemetry.io/otel/trace v1.33.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/exp v0.0.0-20260727155853-b88d891fe743 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	tinygo.org/x/espradio v0.3.0 // indirect
)

replace tinygo.org/x/bluetooth => ./third_party/tinygo-bluetooth
