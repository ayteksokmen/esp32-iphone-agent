tinygo.org/x/bluetooth v0.16.0 with two changes. In adapter_darwin.go: DefaultAdapter creates its CoreBluetooth
managers in Enable() instead of at program start, so esp32agent commands that never use Bluetooth (everything but
the daemon) do not need the Bluetooth permission on macOS. In gap_darwin.go: Connect returned a zero Device and
no error when the peer disconnected before the link came up, and the daemon crashed on it (2026-11-12); it now
returns an error. Examples, tools, tests and the nRF SoftDevice blobs are
left out. Used through the replace directive in ../../go.mod.
