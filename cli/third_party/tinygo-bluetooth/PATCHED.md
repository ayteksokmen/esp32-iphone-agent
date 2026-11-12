tinygo.org/x/bluetooth v0.16.0 with one change, in adapter_darwin.go: DefaultAdapter creates its CoreBluetooth
managers in Enable() instead of at program start, so esp32agent commands that never use Bluetooth (everything but
the daemon) do not need the Bluetooth permission on macOS. Examples, tools, tests and the nRF SoftDevice blobs are
left out. Used through the replace directive in ../../go.mod.
