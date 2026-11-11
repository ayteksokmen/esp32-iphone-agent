// Status screen for boards with a built-in display. Only the Waveshare ESP32-S3-Touch-AMOLED-2.16
// (480x480 CO5300 over QSPI) for now; build with `flash.sh --amoled216` (-DBOARD_AMOLED_216=1).
// Other builds compile none of this.
#pragma once
#include <Arduino.h>

struct ScreenInfo {
  String fw;       // firmware version
  String usb;      // "ok" | "asleep" | "off"
  String net;      // "ap" | "sta" | "none"
  String ssid;     // hotspot name in AP mode
  String pass;     // hotspot password in AP mode
  String ip;
  bool ble = false;
  bool cli = false;
  int airplay = 0;  // AIRPLAY_IDLE / _CONNECTED / _STREAMING
  float fps = 0;
};

bool screen_begin();                    // false when the panel did not come up
void screen_show(const ScreenInfo &s);  // redraws only the lines that changed
