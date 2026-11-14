#pragma once
#include <Arduino.h>

// Firmware update over Wi-Fi: TCP on port 7300, the same per-boot token as the video channel.
// The app sends "ota <token> <size> <md5>\n" and then the image; the board answers "ok\n" to the header,
// writes the image to the other app slot and answers "done\n" (then reboots) or "err <reason>\n".
// A new image must call ota_confirm() once it runs well, or the bootloader goes back to the old one.
bool ota_begin();
bool ota_busy();
void ota_confirm();  // marks the running image good (no-op when it already is)
// The paired CLI's way in (`ota` over its sealed session, docs/cli-protocol.md "Firmware update"): a fresh random
// token, good for one update within OTA_CLI_TTL_MS, never the app's video token. "" when this build has no OTA.
const unsigned long OTA_CLI_TTL_MS = 120000;
String ota_cli_token();
